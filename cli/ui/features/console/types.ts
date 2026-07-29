import type { FormEvent } from "react";
import type { LocalSessionStatus } from "@/lib/api/local-client";
import type { ConsoleState, ServiceRecord } from "@/lib/contracts/registry";
import type { ConsoleRoute } from "@/features/console/navigation";
import type { FoundationState } from "@/lib/presentation/project";

export type MutationReview = {
  project: string;
  targetType: string;
  targetID: string;
  operation: string;
  diff: string[];
  risk: string;
  confirmation?: string;
  idempotencyKey: string;
  status: "reviewing" | "submitting" | "succeeded" | "failed";
  evidence?: string;
  error?: string;
  nextAction?: string;
};

export type MutationRequest = Omit<MutationReview, "idempotencyKey" | "status" | "evidence" | "error" | "nextAction">;

export type ConsoleController = {
  active: string;
  route: ConsoleRoute;
  session: LocalSessionStatus | null;
  review: MutationReview | null;
  navigate: (route: Partial<ConsoleRoute>) => void;
  setActive: (view: string) => void;
  setProjectID: (id: string) => void;
  setServiceDetail: (service: ServiceRecord | null) => void;
  reviewMutation: (request: MutationRequest, submit: (idempotencyKey: string) => Promise<string>) => void;
  closeReview: () => void;
  submitReview: () => Promise<void>;
  state: ConsoleState & FoundationState;
  actions: {
    addServer: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    createProject: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    createService: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    diagnostics: (nodeID: string) => Promise<void>;
    load: () => Promise<void>;
    loadBootstrapEvents: (sessionID: string) => Promise<void>;
    retryBootstrap: (sessionID: string) => void;
    loadDeploymentEvents: (deploymentID: string) => Promise<void>;
    incidentList: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    incidentGet: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    incidentResolve: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    nodeAction: (nodeID: string, action: "offline" | "drain" | "remove") => void;
    rollback: (deploymentID: string) => void;
    secretCreate: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    secretReveal: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    secretRotate: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    setupTOTP: () => void;
  };
};
