package config

// Env is the Copilot/orchestrator environment every tool call names explicitly. There is
// deliberately no default: never assume one.
type Env string

const (
	Prod    Env = "prod"
	PreProd Env = "pre_prod"
)

// Envs lists every Env, in the order tool schemas advertise them.
var Envs = []Env{Prod, PreProd}
