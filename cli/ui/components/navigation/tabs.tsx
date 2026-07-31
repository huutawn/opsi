import type { KeyboardEvent } from "react";

export function Tabs({ label, items, selected, onSelect }: {
  label: string;
  items: ReadonlyArray<{ id: string; label: string; href: string }>;
  selected: string;
  onSelect: (event: React.MouseEvent<HTMLAnchorElement>, id: string) => void;
}) {
  function move(event: KeyboardEvent<HTMLAnchorElement>, index: number) {
    const tabs = Array.from(event.currentTarget.closest('[role="tablist"]')?.querySelectorAll<HTMLAnchorElement>('[role="tab"]') ?? []);
    let next = index;
    if (event.key === "ArrowRight") next = (index + 1) % tabs.length;
    else if (event.key === "ArrowLeft") next = (index - 1 + tabs.length) % tabs.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = tabs.length - 1;
    else if (event.key === " ") {
      event.preventDefault();
      event.currentTarget.click();
      return;
    } else return;
    event.preventDefault();
    tabs[next]?.focus();
  }

  return <nav aria-label={label} className="tabs"><div role="tablist">{items.map((item, index) => {
    const active = selected === item.id;
    return <a aria-selected={active} className={active ? "active" : ""} href={item.href} key={item.id} onClick={(event) => onSelect(event, item.id)} onKeyDown={(event) => move(event, index)} role="tab" tabIndex={active ? 0 : -1}>{item.label}</a>;
  })}</div></nav>;
}
