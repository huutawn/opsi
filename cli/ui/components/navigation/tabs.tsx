import React, { KeyboardEvent, MouseEvent } from "react";
import Link from "next/link";

export interface TabItem {
  id: string;
  label: string;
  href: string;
}

export interface TabsProps {
  items: TabItem[];
  label: string;
  selected: string;
  onSelect: (event: MouseEvent<HTMLAnchorElement>, tabId: string) => void;
}

export function Tabs({ items, label, selected, onSelect }: TabsProps) {
  function handleKeyDown(event: KeyboardEvent<HTMLElement>) {
    const keys = ["ArrowRight", "ArrowLeft", "Home", "End"];
    if (!keys.includes(event.key)) return;
    const currentIndex = items.findIndex((item) => item.id === selected);
    if (currentIndex === -1) return;
    let nextIndex = currentIndex;
    if (event.key === "ArrowRight") nextIndex = (currentIndex + 1) % items.length;
    else if (event.key === "ArrowLeft") nextIndex = (currentIndex - 1 + items.length) % items.length;
    else if (event.key === "Home") nextIndex = 0;
    else if (event.key === "End") nextIndex = items.length - 1;
    const nextItem = items[nextIndex];
    if (nextItem) {
      event.preventDefault();
      const target = document.getElementById(`tab-${nextItem.id}`);
      target?.click();
      target?.focus();
    }
  }

  return (
    <div className="border-b border-outline-variant/20 mb-6 overflow-x-auto max-w-full">
      <nav className="flex space-x-6 min-w-max" aria-label={label} role="tablist" onKeyDown={handleKeyDown}>
        {items.map((tab) => {
          const isSelected = tab.id === selected;
          return (
            <Link
              key={tab.id}
              href={tab.href}
              prefetch={false}
              onClick={(e) => onSelect(e, tab.id)}
              className={`
                whitespace-nowrap py-3 px-1 border-b-2 font-medium text-sm transition-colors
                ${
                  isSelected
                    ? "border-primary text-primary"
                    : "border-transparent text-on-surface-variant hover:text-on-surface hover:border-outline-variant/50"
                }
              `}
              role="tab"
              aria-selected={isSelected}
              aria-controls={`panel-${tab.id}`}
              id={`tab-${tab.id}`}
            >
              {tab.label}
            </Link>
          );
        })}
      </nav>
    </div>
  );
}

export function tabPanelProps(label: string, id: string) {
  return {
    role: "tabpanel",
    id: `panel-${id}`,
    "aria-labelledby": `tab-${id}`,
    "aria-label": label,
    className: "focus:outline-none",
    tabIndex: 0,
  };
}
