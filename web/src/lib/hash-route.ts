import { useEffect, useState } from "react";

export function hashParameters(page: string): URLSearchParams {
  const [active, query = ""] = location.hash.slice(1).split("?", 2);
  return new URLSearchParams(active === page ? query : "");
}

export function useHashParameters(page: string): URLSearchParams {
  const [parameters, setParameters] = useState(() => hashParameters(page));
  useEffect(() => {
    const changed = () => setParameters(hashParameters(page));
    changed();
    window.addEventListener("hashchange", changed);
    return () => window.removeEventListener("hashchange", changed);
  }, [page]);
  return parameters;
}
