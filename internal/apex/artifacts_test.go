package apex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

func stubArtifacts(t *testing.T) (f *fakeApex, root string) {
	t.Helper()
	f = newFakeApex(t, nil)
	logA, trace := artifactFixture("job.log", "logs", 11), artifactFixture("trace.zip", "other", 4)
	f.on("GET", "/jobs/"+jobID+"/artifacts", jsonReply([]Artifact{logA, trace}))
	f.on("GET", strings.TrimPrefix(logA.DownloadURL, apiPrefix), reply{status: 200, raw: []byte("log content")})
	f.on("GET", strings.TrimPrefix(trace.DownloadURL, apiPrefix), reply{status: 200, raw: []byte("ZIP!")})
	return f, t.TempDir()
}

func TestListJobArtifactsListsFilenameTypeAndSize(t *testing.T) {
	stubArtifacts(t)
	folder, arts, err := ListJobArtifacts(t.Context(), config.PreProd, jobID, "")
	want := []ArtifactInfo{{"job.log", "logs", 11}, {"trace.zip", "other", 4}}
	if err != nil || folder != "Authorization Test" || len(arts) != 2 || arts[0] != want[0] || arts[1] != want[1] {
		t.Errorf("ListJobArtifacts = %q %v %v, want the test folder and %v", folder, arts, err, want)
	}
}

func TestListJobArtifactsHonoursAFolderOverride(t *testing.T) {
	f, _ := stubArtifacts(t)
	if _, _, err := ListJobArtifacts(t.Context(), config.PreProd, jobID, "Authorization"); err != nil {
		t.Fatal(err)
	}
	if got := f.callsTo("GET", "/jobs/"+jobID+"/artifacts")[0].Header.Get("X-Folder-Id"); got != folderIDs["Authorization"] {
		t.Errorf("X-Folder-Id = %q, want the override folder", got)
	}
}

func TestDownloadWritesTheNamedFilesUnderRootJobIDAndReturnsPathsAndSizes(t *testing.T) {
	_, root := stubArtifacts(t)
	dir, files, err := DownloadJobArtifacts(t.Context(), config.PreProd, jobID, []string{"trace.zip"}, false, root, "")
	if err != nil {
		t.Fatal(err)
	}
	want := DownloadedArtifact{Filename: "trace.zip", Path: filepath.Join(root, jobID, "trace.zip"), SizeBytes: 4}
	if dir != filepath.Join(root, jobID) || len(files) != 1 || files[0] != want {
		t.Errorf("dir = %q files = %+v, want %+v", dir, files, want)
	}
	if b, _ := os.ReadFile(want.Path); string(b) != "ZIP!" {
		t.Errorf("content = %q, want ZIP!", b)
	}
	if _, err := os.Stat(filepath.Join(root, jobID, "job.log")); !os.IsNotExist(err) {
		t.Error("an unrequested artifact was downloaded")
	}
}

func TestDownloadAllFetchesEveryListedArtifactAndLeavesNoTempFiles(t *testing.T) {
	_, root := stubArtifacts(t)
	dir, files, err := DownloadJobArtifacts(t.Context(), config.PreProd, jobID, nil, true, root, "")
	if err != nil || len(files) != 2 || files[0].Filename != "job.log" || files[1].Filename != "trace.zip" {
		t.Fatalf("DownloadJobArtifacts = %+v, %v", files, err)
	}
	if b, _ := os.ReadFile(files[0].Path); string(b) != "log content" {
		t.Errorf("job.log = %q", b)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".download-") {
			t.Errorf("temp file %s left behind", e.Name())
		}
	}
	if len(entries) != 4 { // two artifacts + their two content-free sidecars
		t.Errorf("dir has %d entries, want 4", len(entries))
	}
}

func TestDownloadDirIsPrivate0700AndFilesAreOwnerOnly0600(t *testing.T) {
	_, root := stubArtifacts(t)
	dir, _, err := DownloadJobArtifacts(t.Context(), config.PreProd, jobID, []string{"job.log"}, false, root, "")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, root: 0o700, filepath.Join(dir, "job.log"): 0o600} {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != want {
			t.Errorf("%s mode = %v (%v), want %v", path, st.Mode().Perm(), err, want)
		}
	}
}

func TestDownloadFailureLeavesNoPartialFileUnderTheRealName(t *testing.T) {
	f, root := stubArtifacts(t)
	f.on("GET", strings.TrimPrefix(artifactFixture("trace.zip", "other", 4).DownloadURL, apiPrefix), jsonReply(map[string]any{"detail": "gone"}, 404))
	dir, _, err := DownloadJobArtifacts(t.Context(), config.PreProd, jobID, []string{"trace.zip"}, false, root, "")
	if err == nil {
		t.Fatal("download succeeded though the server answered 404")
	}
	entries, _ := os.ReadDir(filepath.Join(root, jobID))
	if len(entries) != 0 {
		t.Errorf("%s holds %d leftover entries, want none", dir, len(entries))
	}
}

func TestDownloadUnknownNameListsWhatExistsBeforeAnyDownload(t *testing.T) {
	f, root := stubArtifacts(t)
	_, _, err := DownloadJobArtifacts(t.Context(), config.PreProd, jobID, []string{"job.log", "nope.png"}, false, root, "")
	if !shared.IsExpected(err) || !strings.Contains(err.Error(), "'nope.png'") || !strings.Contains(err.Error(), "job.log, trace.zip") {
		t.Errorf("error = %v, want an expected error naming nope.png and the available files", err)
	}
	for _, c := range f.callsTo("GET", "/jobs/") {
		if strings.Contains(c.Path, "/artifacts/") {
			t.Errorf("downloaded %s though a requested name was unknown", c.Path)
		}
	}
	if _, err := os.Stat(filepath.Join(root, jobID)); !os.IsNotExist(err) {
		t.Error("a job dir was created before the names were validated")
	}
}

func TestDownloadRefusesAJobIDThatIsNotAPlainID(t *testing.T) {
	f, root := stubArtifacts(t)
	_, _, err := DownloadJobArtifacts(t.Context(), config.PreProd, "../../etc", nil, true, root, "")
	if !shared.IsExpected(err) {
		t.Errorf("error = %v, want an expected refusal", err)
	}
	if n := len(f.callsTo("GET", "/jobs/")); n != 0 {
		t.Errorf("%d job requests made, want 0", n)
	}
}

func TestDownloadRefusesAServerSuppliedFilenameContainingAPath(t *testing.T) {
	f, root := stubArtifacts(t)
	evil := artifactFixture("../escape.txt", "other", 1)
	f.on("GET", "/jobs/"+jobID+"/artifacts", jsonReply([]Artifact{evil}))
	_, _, err := DownloadJobArtifacts(t.Context(), config.PreProd, jobID, nil, true, root, "")
	if err == nil || !strings.Contains(err.Error(), "path in its name") {
		t.Errorf("error = %v, want the path-in-name refusal", err)
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(err) {
		t.Error("a file escaped the artifacts root")
	}
}

func TestDefaultArtifactsRootIsUnderTheUsersCacheDir(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	got, err := DefaultArtifactsRoot()
	if want := filepath.Join("/home/someone", ".cache", "copilot-mcp", "artifacts"); err != nil || got != want {
		t.Errorf("DefaultArtifactsRoot = %q, %v, want %q", got, err, want)
	}
}
