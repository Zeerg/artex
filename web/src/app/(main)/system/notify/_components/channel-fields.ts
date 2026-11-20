// A tool for parsing channel field tables and configuration values.
//
// Separated from the page because this one is**Data**Instead of view: it describes which fields each channel has,
// What controls to use, and bidirectional conversion between form text and configuration values (JSON).
// Placing a file separately, only this needs to be modified for new channels. The page itself does not need to change.
// Channel type display name and introduction. Placed on the frontend because it only affects text, backend does not need to know.
export const KIND_LABEL: Record<string, string> = {
 dingtalk: "DingTalk",
 feishu: "Feishu",
 wecom: "Enterprise WeChat",
 webhook: "general Webhook",
 telegram: "Telegram",
 email: "Selected",
};

// Configuration field definitions for each channel.
//
// We intentionally keep a front-end field table here instead of letting the back-end send a schema; the back-end only handles
// Validate (Required/Format), UI needs layout and control types, both focus on different things.
// The only coupling point is secret_keys —— Which fields should be rendered as password boxes, determined by the backend.
// Because only channels know which values are credentials (the entire WeChat Work Webhook is a credential,
// And DingTalk is just one of its secrets). Adding a channel here without an entry will make the form blank,
// 'Will not silently fail (the hasFields below will prompt)',
export type FieldKind = "text" | "password" | "number" | "select" | "textarea" | "switch" | "kv" | "list";
export interface FieldDef {
 key: string;
 label: string;
 kind: FieldKind;
 placeholder?: string;
 help?: string;
 options?: { value: string; label: string }[];
}
export const CHANNEL_FIELDS: Record<string, FieldDef[]> = {
 dingtalk: [
 {
 key: "webhook",
 label: "Webhook Address",
 kind: "text",
 placeholder: "https://oapi.dingtalk.com/robot/send?access_token=...",
 },
 {
 key: "secret",
 label: "Sign Key",
 kind: "password",
 help: "When robot security settings are selected as 'signing', fill in the custom keyword or leave it empty if the security setting is not enabled",
 },
 ],
 feishu: [
 {
 key: "webhook",
 label: "Webhook Address",
 kind: "text",
 placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
 },
 { key: "secret", label: "Signature Verification Key", kind: "password", help: "Fill in when robot is enabled for 'signature verification', otherwise leave empty" },
 ],
 wecom: [
 {
 key: "webhook",
 label: "Webhook Address",
 kind: "text",
 placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...",
 },
 ],
 webhook: [
 { key: "url", label: "Target URL", kind: "text", placeholder: "https://your-endpoint.example.com/hook" },
 {
 key: "method",
 label: "Request Method",
 kind: "select",
 options: [
 { value: "POST", label: "POST (With request body)" },
 { value: "PUT", label: "PUT (With request body)" },
 { value: "PATCH", label: "PATCH (With request body)" },
 { value: "GET", label: "GET (Without request body)" },
 ],
 },
 { key: "headers", label: "Custom request headers", kind: "kv", help: "Each Line KEY=VALUE, For example, Authorization=Bearer xxx" },
 {
 key: "body_template",
 label: "Request body template",
 kind: "textarea",
 help:
 "Leave empty to use built-in default template. Variables:{{.Title}} {{.Batch}} {{.Count}} {{.HomeURL}} {{.SentAt}}, " +
 "And range .Items Below .Name/.VulnClass/.Severity/.Summary/.Assets/.DetailURL/.StatusLabel. " +
 "Insert string please use {{json .Xxx}} Rather than {{.Xxx}}, Otherwise, quotes in the title will disrupt JSON.",
 },
 ],
 telegram: [
 { key: "bot_token", label: "Bot Token", kind: "password", placeholder: "123456:ABC-DEF..." },
 { key: "chat_id", label: "Chat ID", kind: "text", placeholder: "-1001234567890" },
 {
 key: "base_url",
 label: "API Address",
 kind: "text",
 placeholder: "https://api.telegram.org",
 help: "Leave empty for official address; fill when setting up custom Bot API reverse proxy.",
 },
 ],
 email: [
 { key: "host", label: "SMTP Server", kind: "text", placeholder: "smtp.example.com" },
 {
 key: "port",
 label: "Port",
 kind: "number",
 placeholder: "587",
 help: "587 Use STARTTLS; 465 enable 'implicit TLS'.",
 },
 { key: "username", label: "Account", kind: "text" },
 { key: "password", label: "Password / Authorization code", kind: "password" },
 { key: "from", label: "Sender", kind: "text", placeholder: "artex@example.com" },
 { key: "to", label: "Recipient", kind: "list", help: "Multiple addresses separated by commas" },
 { key: "tls", label: "Implicit TLS", kind: "switch", help: "465 Port is open; 587 remains closed (will automatically STARTTLS)" },
 ],
};

export const SEVERITY_OPTIONS = [
 { value: "", label: "Unlimited" },
 { value: "low", label: "Low-risk and above." },
 { value: "medium", label: "Medium or higher risk" },
 { value: "high", label: "High-risk and above" },
 { value: "critical", label: "Only severe" },
];

export type ChannelForm = {
 name: string;
 kind: string;
 mode: "realtime" | "digest";
 enabled: boolean;
 ratePerMin: string;
 config: Record<string, unknown>;
 minSeverity: string;
 includeText: string;
 excludeText: string;
 taskIDsText: string;
 assetIDsText: string;
 onStatusChange: boolean;
};

export const emptyForm = (kind: string): ChannelForm => ({
 name: "",
 kind,
 mode: "realtime",
 enabled: true,
 ratePerMin: "",
 config: {},
 minSeverity: "",
 includeText: "",
 excludeText: "",
 taskIDsText: "",
 assetIDsText: "",
 onStatusChange: false,
});

// parseKV Parsing 'KEY per line'=VALUE'of the text area.
export function parseKV(text: string): Record<string, string> {
 const out: Record<string, string> = {};
 for (const line of text.split("\n")) {
 const t = line.trim();
 if (!t) continue;
 const i = t.indexOf("=");
 if (i > 0) out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
 }
 return out;
}
// parseIDs Parse a comma/space-separated list of id values.
export function parseIDs(text: string): number[] {
 return text
 .split(/[\s,, ]+/)
 .map((s) => s.trim())
 .filter(Boolean)
 .map((s) => Number(s))
 .filter((n) => Number.isFinite(n) && n > 0);
}
// parseKeywords Parse line/comma-separated keyword list (vulnerability type names may contain spaces, so cut by line or comma).
export function parseKeywords(text: string): string[] {
 return text
 .split(/[\n,, ]+/)
 .map((s) => s.trim())
 .filter(Boolean);
}
