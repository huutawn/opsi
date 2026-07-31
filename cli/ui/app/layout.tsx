import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Opsi Console",
  description: "Project-first local Opsi operations console",
  icons: { icon: "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E%3Crect width='32' height='32' rx='6' fill='%2320211f'/%3E%3Ccircle cx='16' cy='16' r='8' fill='none' stroke='%23f4f1e8' stroke-width='4'/%3E%3C/svg%3E" },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
