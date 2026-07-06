import type { FormEvent } from "react";
import type { ConsoleState, ServiceRecord } from "@/lib/contracts/registry";

export type ConsoleController = {
  active: string;
  setActive: (view: string) => void;
  setProjectID: (id: string) => void;
  setServiceDetail: (service: ServiceRecord | null) => void;
  state: ConsoleState;
  actions: {
    addServer: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    createProject: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    createService: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    deploy: (serviceID: string) => Promise<void>;
    diagnostics: (nodeID: string) => Promise<void>;
    load: () => Promise<void>;
    loadBootstrapEvents: (sessionID: string) => Promise<void>;
    loadDeploymentEvents: (deploymentID: string) => Promise<void>;
    nodeAction: (nodeID: string, action: "drain" | "remove") => Promise<void>;
    rollback: (deploymentID: string) => Promise<void>;
    secretCreate: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    secretReveal: (event: FormEvent<HTMLFormElement>) => Promise<void>;
    secretRotate: (event: FormEvent<HTMLFormElement>) => Promise<void>;
  };
};
