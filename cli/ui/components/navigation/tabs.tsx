"use client";

import { useEffect, useState, type KeyboardEvent } from "react";

function tabID(label: string, id: string, part: "tab" | "panel") {
  return `${label.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/(^-|-$)/g, "")}-${part}-${id}`;
}

export function tabPanelProps(label: string, id: string) {
  return { "aria-labelledby": tabID(label, id, "tab"), id: tabID(label, id, "panel"), role: "tabpanel" as const, tabIndex: 0 };
}

export function Tabs({ label, items, selected, onSelect }: {
  label: string;
  items: ReadonlyArray<{ id: string; label: string; href: string }>;
  selected: string;
  onSelect: (event: React.MouseEvent<HTMLAnchorElement>, id: string) => void;
}) {
  const [focused, setFocused] = useState(selected);
  useEffect(() => {
    // Keep roving focus aligned when browser history changes the selected tab.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setFocused(selected);
  }, [selected]);

  function move(event: KeyboardEvent<HTMLAnchorElement>, index: number) {
    const tabs = Array.from(event.currentTarget.closest('[role="tablist"]')?.querySelectorAll<HTMLAnchorElement>('[role="tab"]') ?? []);
    let next = index;
    if (event.key === "ArrowRight") next = (index + 1) % tabs.length;
    else if (event.key === "ArrowLeft") next = (index - 1 + tabs.length) % tabs.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = tabs.length - 1;
    else if (event.key === " " || event.key === "Enter") {
      event.preventDefault();
      event.currentTarget.click();
      return;
    } else return;
    event.preventDefault();
    setFocused(items[next]?.id ?? selected);
    tabs[next]?.focus();
  }

  return <nav aria-label={label} className="tabs"><div role="tablist">{items.map((item, index) => {
    const active = selected === item.id;
    return <a aria-controls={tabID(label, item.id, "panel")} aria-selected={active} className={active ? "active" : ""} href={item.href} id={tabID(label, item.id, "tab")} key={item.id} onClick={(event) => { setFocused(item.id); onSelect(event, item.id); }} onKeyDown={(event) => move(event, index)} role="tab" tabIndex={focused === item.id ? 0 : -1}>{item.label}</a>;
  })}</div></nav>;
}
