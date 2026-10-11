// Which backend serves each orchestrator value. ENABLED_ORCHESTRATORS gates what the tools
// expose; retiring a backend = delete its directory, its entry here and its ORCHESTRATORS value.

import { uipathBackend } from "../uipath/backend.js";
import type { OrchestratorBackend } from "./backend.js";
import type { Orchestrator } from "./types.js";

export const ENABLED_ORCHESTRATORS = ["uipath"] as const satisfies readonly Orchestrator[];
export type EnabledOrchestrator = (typeof ENABLED_ORCHESTRATORS)[number];

export const BACKENDS: Record<EnabledOrchestrator, OrchestratorBackend> = {
  uipath: uipathBackend,
};

export const backendFor = (o: EnabledOrchestrator): OrchestratorBackend => BACKENDS[o];
