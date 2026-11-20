import { fileURLToPath } from "node:url";

// Static export: `NEXT_EXPORT=1 next build` Output pure static directory to web/out, can be directly dropped into
// nginx web Run from the root directory. Development(next dev)Do not set this variable to retain /api reverse proxy and hot updates.
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel demo: The entire site runs on mock, no backend, no need for /api reverse proxy.
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

/** @type {import('next').NextConfig} */
const nextConfig = {
 // Avoid lockfile in parent directory affecting root directory inference and resource path generation.
 turbopack: { root: fileURLToPath(new URL(".", import.meta.url)) },
 reactCompiler: true,
 // Allow access to dev resources (HMR) from local network IPs; add or remove as needed.
 // dev Phase to allow any IPv4 source access to /_next/* Works with HMR (changes in local network IP do not affect it).
 // Note: Next prohibits raw due to security reasons "*", Need segmented wildcard;"*.*.*.*" matches any IPv4.
 allowedDevOrigins: ["*.*.*.*"],
 compiler: {
 removeConsole: process.env.NODE_ENV === "production",
 },
 ...(isExport
? {
 // Pure static export: No Node runtime; images not optimized; each route produces <route>/index.html.
 output: "export",
 images: { unoptimized: true },
 trailingSlash: true,
 }
: isMock
? {
 // Vercel mock demo: No backend, no need for /api reverse proxy.
 images: { unoptimized: true },
 }
: {
 // development: remove /api/* Reverse proxy to Go backend (default:8787, Available AUTOPENTEST_API overrides).
 async rewrites() {
 const backend = process.env.AUTOPENTEST_API?? "http://localhost:8787";
 return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
 },
 }),
};

export default nextConfig;
