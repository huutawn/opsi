export const navGroups = [
  ["Setup", "Projects", "Servers / Nodes"],
  ["Operate", "Overview", "Services", "Deployments", "Incidents & RCA"],
  ["Understand", "Topology", "Logs", "Metrics"],
  ["Control", "Secrets", "Audit", "Settings"],
] as const;

export type ConsoleView = (typeof navGroups)[number][number] | "Projects";
