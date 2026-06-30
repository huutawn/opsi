import type { Project } from "@/lib/contracts/registry";

export function ProjectPicker({
  project,
  projects,
  onSelect,
}: {
  project: Project | null;
  projects: Project[];
  onSelect: (projectID: string) => void;
}) {
  if (!projects.length) return null;
  return (
    <div className="projectPicker">
      <select className="select" onChange={(event) => onSelect(event.target.value)} value={project?.id ?? ""}>
        {projects.map((item) => (
          <option key={item.id} value={item.id}>
            {item.name}
          </option>
        ))}
      </select>
    </div>
  );
}
