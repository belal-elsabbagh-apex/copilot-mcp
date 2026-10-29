package apex

// get_job_artifacts: list a job's artifacts, or download chosen ones to a private cache dir.
// Artifacts (screenshots, trace.zip, recordings, job.log) can contain PHI, so they are written
// only under ~/.cache/copilot-mcp/artifacts/<jobId>/ (dir 0700, files 0600) — never into a repo —
// and only the paths come back, not the bytes. Files are streamed to a temp file in the target
// dir and renamed into place (a trace.zip can be large; nothing is buffered, and a failed
// download never leaves a partial file under the real name).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/memo"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// ArtifactInfo is one listed artifact.
type ArtifactInfo struct {
	Filename  string `json:"filename"`
	Type      string `json:"type"` // logs | videos | other
	SizeBytes int64  `json:"sizeBytes"`
}

// DownloadedArtifact is one artifact written to disk.
type DownloadedArtifact struct {
	Filename  string `json:"filename"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
}

// DefaultArtifactsRoot is ~/.cache/copilot-mcp/artifacts.
func DefaultArtifactsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "copilot-mcp", "artifacts"), nil
}

// safeJobID: the job id becomes a directory name, so it must not be able to climb out of the root.
var safeJobID = regexp.MustCompile(`^[\w-]+$`)

// isBareFilename: server-supplied filenames are a bare name only, never a path.
func isBareFilename(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`)
}

// ListJobArtifacts lists a job's artifacts; folder "" = the env's folder.
func ListJobArtifacts(ctx context.Context, env config.Env, jobID, folder string) (folderName string, artifacts []ArtifactInfo, err error) {
	var (
		resolved Folder
		rerr     error
		wg       sync.WaitGroup
	)
	wg.Go(func() { resolved, rerr = ResolveFolderID(ctx, env, folder) })
	rows, err := listArtifacts(ctx, env, folder, jobID)
	wg.Wait()
	if rerr != nil {
		return "", nil, rerr
	}
	if err != nil {
		return "", nil, err
	}
	artifacts = make([]ArtifactInfo, len(rows))
	for i, a := range rows {
		artifacts[i] = ArtifactInfo{Filename: a.Filename, Type: a.ArtifactType, SizeBytes: a.SizeBytes}
	}
	return resolved.Name, artifacts, nil
}

func chooseArtifacts(rows []Artifact, names []string, all bool) ([]Artifact, error) {
	if all {
		return rows, nil
	}
	has := func(n string) bool {
		return slices.ContainsFunc(rows, func(a Artifact) bool { return a.Filename == n })
	}
	var missing []string
	for _, n := range names {
		if !has(n) {
			missing = append(missing, "'"+n+"'")
		}
	}
	if len(missing) > 0 {
		avail := make([]string, len(rows))
		for i, a := range rows {
			avail[i] = a.Filename
		}
		list := strings.Join(avail, ", ")
		if list == "" {
			list = "(none)"
		}
		return nil, shared.Expectedf("no artifact named %s on this job; available: %s", strings.Join(missing, ", "), list)
	}
	return slices.DeleteFunc(slices.Clone(rows), func(a Artifact) bool { return !slices.Contains(names, a.Filename) }), nil
}

// DownloadJobArtifacts downloads the named artifacts (all=true: every one) of a job into
// rootDir/<jobId>/ ("" rootDir = DefaultArtifactsRoot). folder is only the listing's folder-name
// override; downloads need Bearer, not a folder header.
func DownloadJobArtifacts(ctx context.Context, env config.Env, jobID string, names []string, all bool, rootDir, folder string) (dir string, files []DownloadedArtifact, err error) {
	if !safeJobID.MatchString(jobID) {
		return "", nil, shared.Expectedf("'%s' is not a valid job id", jobID)
	}
	rows, err := listArtifacts(ctx, env, folder, jobID)
	if err != nil {
		return "", nil, err
	}
	chosen, err := chooseArtifacts(rows, names, all)
	if err != nil {
		return "", nil, err
	}
	if i := slices.IndexFunc(chosen, func(a Artifact) bool { return !isBareFilename(a.Filename) }); i >= 0 {
		return "", nil, shared.Expectedf("refusing artifact with a path in its name: '%s'", chosen[i].Filename)
	}
	if rootDir == "" {
		if rootDir, err = DefaultArtifactsRoot(); err != nil {
			return "", nil, err
		}
	}
	dir = filepath.Join(rootDir, jobID)
	for _, d := range []string{rootDir, dir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", nil, err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return "", nil, err
		}
	}
	files = []DownloadedArtifact{}
	for _, a := range chosen {
		path := filepath.Join(dir, a.Filename)
		n, err := fetchArtifact(ctx, a, dir, path)
		if err != nil {
			return "", nil, err
		}
		files = append(files, DownloadedArtifact{Filename: a.Filename, Path: path, SizeBytes: n})
	}
	return dir, files, nil
}

// downloadTo streams one artifact into a 0600 temp file in dir, then renames it to path.
func downloadTo(ctx context.Context, a Artifact, dir, path string) (int64, error) {
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op once renamed
	d, err := apexDownload(ctx, a.DownloadURL, "", tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	return d.Bytes, os.Rename(tmp.Name(), path)
}

// sidecar is the content-free record next to a downloaded artifact: which listed artifact the
// file came from. It holds ids and a size only, never bytes of the artifact.
type sidecar struct {
	ArtifactID string `json:"artifactId"`
	SizeBytes  int64  `json:"sizeBytes"`
}

func sidecarPath(dir, filename string) string { return filepath.Join(dir, "."+filename+".meta.json") }

// fetchArtifact skips the download when the file exists AND its sidecar matches the FRESHLY
// listed artifact (id and size). Disk alone is never trusted: a still-running job's artifacts can
// be partial, and the same filename can be re-uploaded under a new id. A memo.Refresh ctx
// (get_job_artifacts refresh=true) always re-downloads.
func fetchArtifact(ctx context.Context, a Artifact, dir, path string) (int64, error) {
	want := sidecar{ArtifactID: a.ID, SizeBytes: a.SizeBytes}
	meta := sidecarPath(dir, a.Filename)
	if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && !memo.Refresh(ctx) { // refresh: re-download even on a match
		var have sidecar
		if b, err := os.ReadFile(meta); err == nil && json.Unmarshal(b, &have) == nil && have == want {
			return st.Size(), nil
		}
	}
	_ = os.Remove(meta) // never leave a sidecar describing a file that is about to be replaced
	n, err := downloadTo(ctx, a, dir, path)
	if err != nil {
		return 0, err
	}
	// Best effort: a missing sidecar only costs a re-download next time.
	if b, err := json.Marshal(want); err == nil {
		_ = os.WriteFile(meta, b, 0o600)
	}
	return n, nil
}
