import { createContext, useContext, type ReactNode } from "react";
import { api } from "./api";
import { useResource } from "./hooks";
import type { Domain, DomainInfo, Meta } from "./types";
import { ErrorBox, Spinner } from "../components/ui";

const MetaContext = createContext<Meta | null>(null);

/** Loads the metric catalog (domains, classes, ranges) once for the session. */
export function MetaProvider({ children }: { children: ReactNode }) {
  const res = useResource((s) => api.get<Meta>("/meta", undefined, s), []);
  if (res.error) return <div className="p-6"><ErrorBox message={res.error} onRetry={res.reload} /></div>;
  if (!res.data) return <div className="p-6"><Spinner /></div>;
  return <MetaContext.Provider value={res.data}>{children}</MetaContext.Provider>;
}

export function useMeta(): Meta {
  const m = useContext(MetaContext);
  if (!m) throw new Error("useMeta outside MetaProvider");
  return m;
}

export function useDomainInfo(d: Domain): DomainInfo | undefined {
  return useMeta().domains.find((x) => x.domain === d);
}
