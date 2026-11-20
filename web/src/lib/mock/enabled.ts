// Mock Switch. Public variables injected during the build phase (variables prefixed with NEXT_PUBLIC_ are readable in the browser).
// Vercel Set NEXT_PUBLIC_MOCK.=1 Entire site uses mock, no backend needed.
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
