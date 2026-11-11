import { useCallback, useEffect, useState } from "react";

/** Loads data with fn; reload() runs it again. deps re-run it automatically. */
export function useAsync<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  const [state, setState] = useState<{ data?: T; error?: Error; loading: boolean }>({ loading: true });
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let live = true;
    setState((s) => ({ ...s, loading: true, error: undefined }));
    fn().then(
      (data) => live && setState({ data, loading: false }),
      (error: Error) => live && setState({ error, loading: false }),
    );
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick]);
  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { ...state, reload };
}

/** Reads and writes one URL query parameter (state lives in the URL). */
export function useQueryParam(name: string, fallback = ""): [string, (v: string) => void] {
  const read = () => new URLSearchParams(location.search).get(name) ?? fallback;
  const [value, setValue] = useState(read);
  useEffect(() => {
    const on = () => setValue(read());
    addEventListener("popstate", on);
    return () => removeEventListener("popstate", on);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name]);
  const set = useCallback(
    (v: string) => {
      const p = new URLSearchParams(location.search);
      if (v && v !== fallback) p.set(name, v);
      else p.delete(name);
      const qs = p.toString();
      history.replaceState(null, "", location.pathname + (qs ? "?" + qs : ""));
      setValue(v);
    },
    [name, fallback],
  );
  return [value, set];
}
