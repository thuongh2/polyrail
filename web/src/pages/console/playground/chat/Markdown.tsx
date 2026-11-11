import { memo, useState, type ComponentProps, type ReactElement, type ReactNode } from "react";
import Markdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { ImageIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { CopyIcon } from "./CopyIcon";

// Assistant Markdown: GFM, never raw HTML (skipHtml), links only http(s)/mailto, images only https
// and only after a click, [n] citations as chips when the answer was grounded (X-GW-KB-Sources).

/** Keeps only http(s) and mailto links and https images; anything else becomes "" (rendered inert). */
export function safeUrl(url: string, key: string) {
  const u = url.trim();
  if (key === "src") return /^https:\/\//i.test(u) ? u : "";
  return /^(https?:\/\/|mailto:)/i.test(u) ? u : "";
}

type MdNode = { type: string; value?: string; children?: MdNode[]; data?: Record<string, unknown> };

/** remark plugin: "[3]" in text (not code or links) → a cite node rendered as <sup data-cite="3">, for 1..max. */
function remarkCite(opts: { max: number }) {
  const walk = (n: MdNode) => {
    if (!n.children || n.type === "link" || n.type === "linkReference") return;
    n.children = n.children.flatMap((c): MdNode[] => {
      if (c.type !== "text" || !c.value) {
        walk(c);
        return [c];
      }
      const out: MdNode[] = [];
      let last = 0;
      for (const m of c.value.matchAll(/\[(\d{1,2})\]/g)) {
        const k = Number(m[1]);
        if (k < 1 || k > opts.max) continue;
        if (m.index! > last) out.push({ type: "text", value: c.value.slice(last, m.index) });
        out.push({ type: "cite", data: { hName: "sup", hProperties: { dataCite: String(k) } }, children: [{ type: "text", value: String(k) }] });
        last = m.index! + m[0].length;
      }
      if (!out.length) return [c];
      if (last < c.value.length) out.push({ type: "text", value: c.value.slice(last) });
      return out;
    });
  };
  return (tree: MdNode) => walk(tree);
}

function CodeBlock({ children }: { children?: ReactNode }) {
  const code = children as ReactElement<{ className?: string; children?: ReactNode }> | undefined;
  const lang = /language-([\w+#.-]+)/.exec(code?.props?.className ?? "")?.[1];
  const text = String(code?.props?.children ?? "").replace(/\n$/, "");
  return (
    <div className="my-3 overflow-hidden rounded-md border bg-muted/40">
      <div className="flex items-center justify-between border-b px-3 py-1 text-xs text-muted-foreground">
        <span>{lang ?? "code"}</span>
        <CopyIcon text={text} label={`Copy ${lang ?? ""} code`} />
      </div>
      <pre className="scrollbar-thin overflow-x-auto p-3 font-mono text-[0.8125rem] leading-relaxed">
        <code>{text}</code>
      </pre>
    </div>
  );
}

/** A remote image stays a placeholder until clicked: loading it would tell its host you read this answer. */
function LazyImage({ src, alt }: { src?: string; alt?: string }) {
  const [show, setShow] = useState(false);
  if (!src) return <span className="text-muted-foreground">[image not shown: only https images can load]</span>;
  let host = src;
  try {
    host = new URL(src).host;
  } catch {
    /* keep src */
  }
  if (show) return <img src={src} alt={alt ?? ""} referrerPolicy="no-referrer" className="my-2 max-h-96 max-w-full rounded-md border" />;
  return (
    <Button type="button" variant="outline" size="sm" className="my-1 h-auto max-w-full py-1.5 whitespace-normal" onClick={() => setShow(true)}>
      <ImageIcon aria-hidden />
      <span className="text-left">
        Load image{alt ? ` "${alt}"` : ""} from {host}
      </span>
    </Button>
  );
}

const base: Components = {
  h1: (p) => <h3 className="mt-5 mb-2 text-lg font-semibold first:mt-0" {...strip(p)} />,
  h2: (p) => <h4 className="mt-5 mb-2 text-base font-semibold first:mt-0" {...strip(p)} />,
  h3: (p) => <h5 className="mt-4 mb-2 text-base font-semibold first:mt-0" {...strip(p)} />,
  h4: (p) => <h6 className="mt-4 mb-1.5 text-sm font-semibold first:mt-0" {...strip(p)} />,
  h5: (p) => <p className="mt-3 mb-1 text-sm font-semibold first:mt-0" {...strip(p)} />,
  h6: (p) => <p className="mt-3 mb-1 text-sm font-medium text-muted-foreground first:mt-0" {...strip(p)} />,
  p: (p) => <p className="my-2 leading-relaxed first:mt-0 last:mb-0" {...strip(p)} />,
  ul: (p) => <ul className="my-2 list-disc space-y-1 pl-5 [&_ol]:my-1 [&_ul]:my-1" {...strip(p)} />,
  ol: (p) => <ol className="my-2 list-decimal space-y-1 pl-5 [&_ol]:my-1 [&_ul]:my-1" {...strip(p)} />,
  li: (p) => <li className="pl-0.5 leading-relaxed [&>p]:my-1" {...strip(p)} />,
  blockquote: (p) => <blockquote className="my-3 border-l-2 pl-3 text-muted-foreground" {...strip(p)} />,
  hr: () => <hr className="my-4" />,
  strong: (p) => <strong className="font-semibold" {...strip(p)} />,
  table: (p) => (
    <div className="scrollbar-thin my-3 overflow-x-auto rounded-md border">
      <table className="w-full text-sm" {...strip(p)} />
    </div>
  ),
  thead: (p) => <thead className="bg-muted/50" {...strip(p)} />,
  th: ({ node: _n, style, ...p }) => <th className="border-b px-3 py-1.5 text-left font-medium" style={style} {...p} />,
  td: ({ node: _n, style, ...p }) => <td className="border-b px-3 py-1.5 align-top [tr:last-child_&]:border-b-0" style={style} {...p} />,
  pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
  code: (p) => <code className="rounded bg-muted px-1 py-0.5 font-mono text-[0.8125rem]" {...strip(p)} />,
  input: ({ node: _n, ...p }) => <input {...p} disabled className="mr-1.5 align-middle" aria-label={p.checked ? "Done" : "Not done"} />,
  a: ({ node: _n, href, children }) =>
    href ? (
      <a href={href} target="_blank" rel="noopener noreferrer nofollow" className="text-signal underline underline-offset-2 hover:no-underline">
        {children}
      </a>
    ) : (
      <span>{children}</span> // unsafe scheme: keep the text, drop the link
    ),
  img: ({ src, alt }) => <LazyImage src={typeof src === "string" ? src : undefined} alt={alt} />,
};

/** Drops react-markdown's `node` prop before spreading onto a DOM element. */
function strip<T extends { node?: unknown }>(p: T): Omit<T, "node"> {
  const { node: _n, ...rest } = p;
  return rest;
}

export type CiteProps = { msgId: string; sources: string[]; label: (ref: string) => string; onCite: (n: number) => void };

function MarkdownBody({ text, cite, className }: { text: string; cite?: CiteProps; className?: string }) {
  const max = cite?.sources.length ?? 0;
  const components: Components = max
    ? {
        ...base,
        sup: ({ node: _n, ...p }: ComponentProps<"sup"> & { node?: unknown; "data-cite"?: string }) => {
          const k = Number(p["data-cite"]);
          if (!k) return <sup {...p} />;
          return (
            <sup>
              <a
                href={`#src-${cite!.msgId}-${k}`}
                onClick={(e) => {
                  e.preventDefault();
                  cite!.onCite(k);
                }}
                className="mx-0.5 inline-flex min-w-4 items-center justify-center rounded border bg-muted px-1 text-[0.6875rem] leading-4 font-medium text-foreground no-underline hover:bg-accent"
                aria-label={`Source ${k}: ${cite!.label(cite!.sources[k - 1])}`}
                title={cite!.label(cite!.sources[k - 1])}
              >
                {k}
              </a>
            </sup>
          );
        },
      }
    : base;
  return (
    <div className={cn("min-w-0 text-sm break-words", className)}>
      <Markdown remarkPlugins={max ? [remarkGfm, [remarkCite, { max }]] : [remarkGfm]} components={components} urlTransform={safeUrl} skipHtml>
        {text}
      </Markdown>
    </div>
  );
}

/** Memoized: while an answer streams only the growing message re-renders. */
export const MarkdownView = memo(MarkdownBody);
