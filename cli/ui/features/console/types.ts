import type { FormEvent } from "react";
import type { LocalSessionStatus } from "@/lib/api/local-client";
import type { BootstrapSession, ConsoleState, ServiceRecord } from "@/lib/contracts/registry";
import type { ConsoleRoute } from "@/features/console/navigation";
import type { FoundationState } from "@/lib/presentation/project";
import type { ProjectSummaryEntry } from "@/lib/presentation/project";

export type MutationReview = {
  project: string;
  targetType: string;
  targetID: string;
  operation: string;
  diff: string[];
  risk: string;
  confirmation?: string;
  credential?: { label: string; inputLabel: string };
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
  setProjectID: (id: string) => Promise<boolean>;
  setServiceDetail: (service: ServiceRecord | null) => void;
  projectSummaries: Record<string, ProjectSummaryEntry>;
  reviewMutation: (request: MutationRequest, submit: (idempotencyKey: string, credential?: string) => Promise<string>) => void;
  closeReview: () => void;
  submitReview: (credential?: string) => Promise<void>;
  state: ConsoleState & FoundationState;
  actions: {
    addServer: (event: FormEvent<HTMLFormElement>, onCreated?: () => void | Promise<void>) => Promise<void>;
    createProject: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    diagnostics: (nodeID: string) => Promise<void>;
    load: () => Promise<void>;
    loadBootstrapEvents: (sessionID: string) => Promise<void>;
    refreshBootstrap: (sessionID: string) => Promise<BootstrapSession | undefined>;
    retryBootstrap: (sessionID: string, onRetried?: () => void | Promise<void>) => void;
    resumeBootstrap: (sessionID: string, probeID: string, authMethod: string, credential: string, username?: string, onResumed?: () => void | Promise<void>) => Promise<string | undefined>;
    loadDeploymentEvents: (deploymentID: string) => Promise<void>;
    incidentList: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    incidentGet: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    incidentResolve: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    hideSensitive: () => void;
    nodeAction: (nodeID: string, action: "offline" | "drain" | "remove") => void;
    rollback: (deploymentID: string) => void;
    secretCreate: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    secretReveal: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    secretRotate: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    setupTOTP: () => void;
  };
};
