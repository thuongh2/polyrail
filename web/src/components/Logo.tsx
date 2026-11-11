/** The Polyrail mark: three rails merging into one. Decorative by default (the name is written next to it). */
export function Logo({ size = 24, className }: { size?: number; className?: string }) {
  return (
    <svg viewBox="0 0 64 64" width={size} height={size} aria-hidden className={className}>
      <rect width="64" height="64" rx="14" className="fill-brand" />
      <g fill="none" strokeWidth={size <= 16 ? 8 : 7} strokeLinecap="round" className="stroke-brand-foreground">
        <path d="M10 14 C28 14 30 32 44 32" />
        <path d="M10 32 H54" />
        <path d="M10 50 C28 50 30 32 44 32" />
      </g>
    </svg>
  );
}
