package config

import (
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Validation walks the generic JSON tree and collects zod-style issues ("path: message") so a
// misconfig names every bad field at once, with the same wording the TS schemas produced.

var (
	uuidRE         = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	orchestratorRE = regexp.MustCompile(`//cloud\.uipath\.com/[^/]+/[^/]+/orchestrator_`)
)

const (
	msgRequired = "Required"
	msgMin1     = "String must contain at least 1 character(s)"
)

type checker struct{ issues []string }

func (c *checker) add(path []string, msg string) {
	where := strings.Join(path, ".")
	if where == "" {
		where = "(root)"
	}
	c.issues = append(c.issues, "  - "+where+": "+msg)
}

func sub(path []string, k string) []string { return append(path[:len(path):len(path)], k) }

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	default:
		return "object"
	}
}

// obj reads m[key] as an object. ok=false means absent or invalid (issue already recorded).
func (c *checker) obj(path []string, m map[string]any, key string, required bool) (map[string]any, bool) {
	v, present := m[key]
	p := sub(path, key)
	if !present {
		if required {
			c.add(p, msgRequired)
		}
		return nil, false
	}
	o, isObj := v.(map[string]any)
	if !isObj {
		c.add(p, "Expected object, received "+typeName(v))
	}
	return o, isObj
}

// strRule describes one string field. Zero value = optional, no extra checks.
type strRule struct {
	required bool
	reqMsg   string // overrides "Required"
	min1     bool
	minMsg   string // overrides the default min-length message
	isURL    bool
	urlMsg   string
}

func pick(custom, def string) string {
	if custom != "" {
		return custom
	}
	return def
}

func isURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme != ""
}

// str validates m[key]; ok=true when present, a string and every rule passed.
func (c *checker) str(path []string, m map[string]any, key string, r strRule) (string, bool) {
	v, present := m[key]
	p := sub(path, key)
	if !present {
		if r.required {
			c.add(p, pick(r.reqMsg, msgRequired))
		}
		return "", false
	}
	s, isStr := v.(string)
	if !isStr {
		c.add(p, "Expected string, received "+typeName(v))
		return "", false
	}
	ok := true
	if r.min1 && s == "" {
		c.add(p, pick(r.minMsg, msgMin1))
		ok = false
	}
	if r.isURL && !isURL(s) {
		c.add(p, r.urlMsg)
		ok = false
	}
	return s, ok
}

func (c *checker) boolean(path []string, m map[string]any, key string) {
	if v, present := m[key]; present {
		if _, isBool := v.(bool); !isBool {
			c.add(sub(path, key), "Expected boolean, received "+typeName(v))
		}
	}
}

// envStrings validates an optional { prod?, pre_prod? } map of strings.
func (c *checker) envStrings(path []string, m map[string]any, key string, min1 bool) {
	o, ok := c.obj(path, m, key, false)
	if !ok {
		return
	}
	for _, env := range Envs {
		c.str(sub(path, key), o, string(env), strRule{min1: min1})
	}
}

func (c *checker) creds(path []string, m map[string]any, key string, required bool) {
	o, ok := c.obj(path, m, key, required)
	if !ok {
		return
	}
	p := sub(path, key)
	c.str(p, o, "be", strRule{required: true, min1: true, minMsg: "be (batch BE base URL) is required"})
	c.str(p, o, "email", strRule{required: true, min1: true, minMsg: "email is required"})
	c.str(p, o, "password", strRule{required: true, min1: true, minMsg: "password is required"})
	c.str(p, o, "fe", strRule{})
}

// profileEnv: EITHER clinicUid OR be+email+password.
func (c *checker) profileEnv(path []string, m map[string]any, key string) {
	o, ok := c.obj(path, m, key, false)
	if !ok {
		return
	}
	p, before := sub(path, key), len(c.issues)
	clinic, _ := c.str(p, o, "clinicUid", strRule{})
	if _, present := o["clinicUid"]; present && clinic != "" && !uuidRE.MatchString(clinic) {
		c.add(sub(p, "clinicUid"), "clinicUid must be the clinic UUID from list_clinics")
	}
	be, _ := c.str(p, o, "be", strRule{min1: true})
	email, _ := c.str(p, o, "email", strRule{min1: true})
	password, _ := c.str(p, o, "password", strRule{min1: true})
	c.str(p, o, "fe", strRule{})
	if len(c.issues) == before && clinic == "" && (be == "" || email == "" || password == "") {
		c.add(p, "needs either clinicUid (support-account auth) or be+email+password (direct login)")
	}
}

func (c *checker) copilot(root map[string]any) {
	o, ok := c.obj(nil, root, "copilot", true)
	if !ok {
		return
	}
	p := []string{"copilot"}
	if sup, ok := c.obj(p, o, "support", false); ok {
		for _, env := range Envs {
			c.creds(sub(p, "support"), sup, string(env), false)
		}
	}
	c.creds(p, o, "prod", false)
	c.creds(p, o, "pre_prod", false)
	if profiles, ok := c.obj(p, o, "profiles", false); ok {
		for _, name := range slices.Sorted(maps.Keys(profiles)) {
			prof, ok := c.obj(sub(p, "profiles"), profiles, name, false)
			if !ok {
				continue
			}
			for _, env := range Envs {
				c.profileEnv(sub(sub(p, "profiles"), name), prof, string(env))
			}
		}
	}
	c.boolean(p, o, "sessionCache")
}

func (c *checker) uipath(root map[string]any) {
	o, ok := c.obj(nil, root, "uipath", true)
	if !ok {
		return
	}
	p, before := []string{"uipath"}, len(c.issues)
	c.str(p, o, "orchestratorUrl", strRule{
		required: true, reqMsg: "orchestratorUrl is required",
		min1: true, minMsg: "orchestratorUrl must not be empty",
		isURL: true, urlMsg: "orchestratorUrl must be a valid URL",
	})
	if u, isStr := o["orchestratorUrl"].(string); isStr && !orchestratorRE.MatchString(u) {
		c.add(sub(p, "orchestratorUrl"), "orchestratorUrl must look like https://cloud.uipath.com/{org}/{tenant}/orchestrator_")
	}
	bearer, _ := c.str(p, o, "bearer", strRule{min1: true, minMsg: "bearer (UiPath PAT/token) must not be empty"})
	hasOAuth := c.oauth(p, o)
	c.str(p, o, "folderPath", strRule{min1: true})
	c.envStrings(p, o, "folderPathByEnv", true)
	c.envStrings(p, o, "folderIdByEnv", true)
	c.str(p, o, "queueUrl", strRule{min1: true})
	c.str(p, o, "addQueueItemPath", strRule{min1: true})
	c.envStrings(p, o, "serverUrlByEnv", false)
	if len(c.issues) == before && bearer == "" && !hasOAuth {
		c.add(p, "uipath requires bearer, oauth, or both")
	}
}

func (c *checker) oauth(path []string, m map[string]any) bool {
	o, ok := c.obj(path, m, "oauth", false)
	if !ok {
		return false
	}
	p := sub(path, "oauth")
	c.str(p, o, "clientId", strRule{required: true, min1: true, minMsg: "oauth.clientId is required"})
	c.str(p, o, "clientSecret", strRule{required: true, min1: true, minMsg: "oauth.clientSecret is required"})
	c.str(p, o, "tokenUrl", strRule{isURL: true, urlMsg: "oauth.tokenUrl must be a valid URL"})
	c.str(p, o, "scope", strRule{min1: true})
	return true
}

func (c *checker) apex(root map[string]any) {
	o, ok := c.obj(nil, root, "apex", false)
	if !ok {
		return
	}
	p := []string{"apex"}
	c.str(p, o, "apiUrl", strRule{isURL: true, urlMsg: "apex.apiUrl must be a valid URL"})
	c.str(p, o, "username", strRule{min1: true})
	c.str(p, o, "password", strRule{min1: true})
	c.envStrings(p, o, "folderByEnv", true)
	c.envStrings(p, o, "serverUrlByEnv", true)
	c.str(p, o, "queueUrl", strRule{min1: true})
}

func (c *checker) feedback(root map[string]any) {
	o, ok := c.obj(nil, root, "feedback", false)
	if !ok {
		return
	}
	p := []string{"feedback"}
	c.boolean(p, o, "enabled")
	c.str(p, o, "repositoryUrl", strRule{isURL: true, urlMsg: "Invalid url"})
}

// validate returns the issue lines (empty = valid).
func validate(parsed any) []string {
	root, ok := parsed.(map[string]any)
	if !ok {
		return []string{"  - (root): Expected object, received " + typeName(parsed)}
	}
	c := &checker{}
	c.copilot(root)
	c.uipath(root)
	c.apex(root)
	c.feedback(root)
	return c.issues
}
