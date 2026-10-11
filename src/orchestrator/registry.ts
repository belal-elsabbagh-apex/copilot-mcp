// Which backend serves each orchestrator value. Retiring a backend = delete its directory,
// its entry here and its ORCHESTRATORS value.

import { apexBackend } from "../apex/backend.js";
import { ApexNotConfiguredError } from "../apex/errors.js";
import { resolveApex } from "../config/config.js";
import { uipathBackend } from "../uipath/backend.js";
import type { OrchestratorBackend } from "./backend.js";
import type { Orchestrator } from "./types.js";

export const BACKENDS: Record<Orchestrator, OrchestratorBackend> = {
  uipath: uipathBackend,
  apex: apexBackend,
};

export const backendFor = (o: Orchestrator): OrchestratorBackend => BACKENDS[o];

// A wholly unconfigured Apex is skipped by "search everything" (UiPath-only setups stay
// quiet); a half-configured one is searched so its ApexNotConfiguredError surfaces.
const apexAbsent = (): boolean => {
  try {
    resolveApex();
    return false;
  } catch (e) {
    return e instanceof ApexNotConfiguredError && !e.partial;
  }
};

export const searchableBackends = (): OrchestratorBackend[] =>
  Object.values(BACKENDS).filter((b) => b.name !== "apex" || !apexAbsent());
