import { useRef, useState, type DragEvent, type FormEvent } from "react";
import { FileText, ImagePlus, Link2, Send, Square, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { bytes } from "../shared";
import type { Attachment, Params } from "./lib";

// Limits: the gateway's default body limit is 8 MB (MAX_BODY_MB) and base64 adds a third.
const MAX_IMAGES = 5;
const MAX_IMAGE_BYTES = 5 * 1024 * 1024;
const MAX_TOTAL_BYTES = 5 * 1024 * 1024;
const MAX_TEXT_BYTES = 200 * 1024;
const IMAGE_TYPES = ["image/png", "image/jpeg", "image/webp", "image/gif"];

const readAs = (f: File, how: "url" | "text") =>
  new Promise<string>((ok, fail) => {
    const r = new FileReader();
    r.onload = () => ok(String(r.result));
    r.onerror = () => fail(r.error);
    if (how === "url") r.readAsDataURL(f);
    else r.readAsText(f);
  });

type Props = {
  input: string;
  setInput: (v: string) => void;
  images: Attachment[];
  setImages: (a: Attachment[]) => void;
  detail: Params["detail"];
  setDetail: (d: Params["detail"]) => void;
  vision: { ok: boolean; why: string };
  busy: boolean;
  disabledWhy: string; // "" when sending is possible
  placeholder: string;
  onSend: () => void;
  onStop: () => void;
};

export function Composer({ input, setInput, images, setImages, detail, setDetail, vision, busy, disabledWhy, placeholder, onSend, onStop }: Props) {
  const [error, setError] = useState("");
  const [urlOpen, setUrlOpen] = useState(false);
  const [url, setUrl] = useState("");
  const [drag, setDrag] = useState(false);
  const imgInput = useRef<HTMLInputElement>(null);
  const textInput = useRef<HTMLInputElement>(null);

  async function addFiles(files: File[]) {
    setError("");
    const imgs = files.filter((f) => f.type.startsWith("image/"));
    const texts = files.filter((f) => !f.type.startsWith("image/"));
    const next = [...images];
    const problems: string[] = [];
    if (imgs.length && !vision.ok) problems.push(vision.why);
    else
      for (const f of imgs) {
        if (!IMAGE_TYPES.includes(f.type)) problems.push(`${f.name}: use PNG, JPEG, WebP or GIF.`);
        else if (f.size > MAX_IMAGE_BYTES) problems.push(`${f.name} is ${bytes(f.size)}; the limit is 5 MB per image.`);
        else if (next.length >= MAX_IMAGES) problems.push(`At most ${MAX_IMAGES} images per message.`);
        else if (next.reduce((s, a) => s + a.size, 0) + f.size > MAX_TOTAL_BYTES) problems.push(`${f.name}: images in one message may total 5 MB (the gateway limits request size).`);
        else next.push({ url: await readAs(f, "url"), name: f.name || "pasted image", size: f.size });
      }
    let text = input;
    for (const f of texts) {
      if (f.size > MAX_TEXT_BYTES) {
        problems.push(`${f.name} is ${bytes(f.size)}; text files up to 200 KB can be inserted.`);
        continue;
      }
      const body = await readAs(f, "text");
      if (body.includes("\u0000")) {
        problems.push(`${f.name} is not a text file.`);
        continue;
      }
      const ext = f.name.split(".").pop() ?? "";
      text += `${text && !text.endsWith("\n") ? "\n\n" : ""}${f.name}:\n\`\`\`${ext}\n${body.replace(/\n$/, "")}\n\`\`\`\n`;
    }
    if (next.length !== images.length) setImages(next);
    if (text !== input) setInput(text);
    setError([...new Set(problems)].join(" "));
  }

  function addUrl(e: FormEvent) {
    e.preventDefault();
    const u = url.trim();
    if (!/^https:\/\/\S+$/i.test(u)) return setError("Image URLs must start with https://.");
    if (images.length >= MAX_IMAGES) return setError(`At most ${MAX_IMAGES} images per message.`);
    setImages([...images, { url: u, name: u.split("/").pop() || u, size: 0 }]);
    setUrl("");
    setUrlOpen(false);
    setError("");
  }

  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setDrag(false);
    if (e.dataTransfer.files.length) addFiles([...e.dataTransfer.files]);
  };

  return (
    <div
      className={cn("border-t p-3", drag && "bg-accent")}
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes("Files")) {
          e.preventDefault();
          setDrag(true);
        }
      }}
      onDragLeave={() => setDrag(false)}
      onDrop={onDrop}
    >
      {images.length > 0 && (
        <div className="mb-2 flex flex-wrap items-end gap-2">
          <ul className="flex flex-wrap gap-2" aria-label="Images to send">
            {images.map((a, i) => (
              <li key={i} className="relative">
                <img src={a.url} alt={a.name} referrerPolicy="no-referrer" className="size-16 rounded-md border object-cover" />
                <Button
                  type="button"
                  variant="secondary"
                  size="icon-xs"
                  className="absolute -top-2 -right-2 rounded-full border"
                  aria-label={`Remove ${a.name}`}
                  onClick={() => setImages(images.filter((_, j) => j !== i))}
                >
                  <X aria-hidden />
                </Button>
                <span className="sr-only">{a.size ? bytes(a.size) : "linked"}</span>
              </li>
            ))}
          </ul>
          <div className="grid gap-1">
            <label htmlFor="pg-detail" className="text-xs text-muted-foreground">
              Detail
            </label>
            <Select value={detail} onValueChange={(v) => setDetail(v as Params["detail"])}>
              <SelectTrigger id="pg-detail" size="sm" className="w-24">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="auto">Auto</SelectItem>
                <SelectItem value="low">Low</SelectItem>
                <SelectItem value="high">High</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
      )}
      {urlOpen && (
        <form onSubmit={addUrl} className="mb-2 flex gap-2">
          <label htmlFor="pg-img-url" className="sr-only">
            Image URL
          </label>
          <Input id="pg-img-url" autoFocus type="url" placeholder="https://example.com/photo.jpg" value={url} onChange={(e) => setUrl(e.target.value)} onKeyDown={(e) => e.key === "Escape" && setUrlOpen(false)} />
          <Button type="submit" variant="outline">
            Add
          </Button>
        </form>
      )}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSend();
        }}
        className="grid gap-2"
      >
        <label htmlFor="pg-input" className="sr-only">
          Message
        </label>
        <Textarea
          id="pg-input"
          rows={2}
          className="max-h-60 min-h-12 resize-none"
          placeholder={placeholder}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          aria-describedby="pg-input-hint"
          onPaste={(e) => {
            const files = [...e.clipboardData.files];
            if (files.some((f) => f.type.startsWith("image/"))) {
              e.preventDefault();
              addFiles(files);
            }
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault();
              if (!busy) onSend();
            }
          }}
        />
        <div className="flex flex-wrap items-center gap-1">
          <input
            ref={imgInput}
            type="file"
            accept={IMAGE_TYPES.join(",")}
            multiple
            hidden
            onChange={(e) => {
              addFiles([...(e.target.files ?? [])]);
              e.target.value = "";
            }}
          />
          <input
            ref={textInput}
            type="file"
            accept=".txt,.md,.markdown,.json,.csv,.tsv,.yaml,.yml,.xml,.html,.log,.py,.js,.ts,.tsx,.go,.java,.rb,.rs,.sql,.sh,text/*"
            hidden
            onChange={(e) => {
              addFiles([...(e.target.files ?? [])]);
              e.target.value = "";
            }}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={vision.ok ? "Attach images" : `Attach images (unavailable: ${vision.why})`}
            title={vision.ok ? "Attach images (or paste / drop them)" : vision.why}
            aria-disabled={!vision.ok}
            className={cn(!vision.ok && "opacity-50")}
            onClick={() => (vision.ok ? imgInput.current?.click() : setError(vision.why))}
          >
            <ImagePlus aria-hidden />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label="Add an image URL"
            title={vision.ok ? "Add an image URL" : vision.why}
            aria-disabled={!vision.ok}
            className={cn(!vision.ok && "opacity-50")}
            onClick={() => (vision.ok ? setUrlOpen(!urlOpen) : setError(vision.why))}
          >
            <Link2 aria-hidden />
          </Button>
          <Button type="button" variant="ghost" size="icon-sm" aria-label="Insert a text file" title="Insert a text file into the message" onClick={() => textInput.current?.click()}>
            <FileText aria-hidden />
          </Button>
          <p id="pg-input-hint" className="ml-1 hidden text-xs text-muted-foreground sm:block">
            Enter to send, Shift+Enter for a new line{busy ? ", Esc to stop" : ""}
          </p>
          <div className="ml-auto flex items-center gap-2">
            {disabledWhy && !busy && <p className="text-xs text-muted-foreground">{disabledWhy}</p>}
            {busy ? (
              <Button type="button" variant="outline" onClick={onStop}>
                <Square aria-hidden />
                Stop
              </Button>
            ) : (
              <Button type="submit" disabled={!!disabledWhy || (!input.trim() && !images.length)}>
                <Send aria-hidden />
                Send
              </Button>
            )}
          </div>
        </div>
        {error && (
          <p role="alert" className="text-xs text-danger">
            {error}
          </p>
        )}
      </form>
    </div>
  );
}
