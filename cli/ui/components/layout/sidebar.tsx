import { navGroups } from "@/features/console/navigation";

export function Sidebar({ active, onSelect }: { active: string; onSelect: (view: string) => void }) {
  return (
    <aside className="sidebar">
      <div className="brand">Opsi</div>
      {navGroups.map(([group, ...items]) => (
        <div key={group}>
          <div className="navGroup">{group}</div>
          {items.map((item) => (
            <button className={`navButton ${active === item ? "active" : ""}`} key={item} onClick={() => onSelect(item)} type="button">
              <span aria-hidden>{item.slice(0, 1)}</span>
              {item}
            </button>
          ))}
        </div>
      ))}
    </aside>
  );
}
