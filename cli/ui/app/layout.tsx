import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Opsi Console",
  description: "Factual infrastructure and delivery operations console",
  icons: { icon: "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E%3Crect width='32' height='32' rx='4' fill='%230f172a'/%3E%3Cpath d='M9 9h14v14H9zM12 12h8v8h-8z' fill='none' stroke='%237bd0ff' stroke-width='2'/%3E%3C/svg%3E" },
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
