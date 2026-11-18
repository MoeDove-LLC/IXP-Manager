import { useState } from "react";
import { X } from "lucide-react";
import { watchKey, type NoteWatch } from "@/lib/note-notifications";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export type LegacyNoteWatchOption = { id: string; createdAt: string; name: string; detail?: string };
export default function LegacyNoteWatchPicker({ label, options, selected, disabled, onChange }: { label: string; options: LegacyNoteWatchOption[]; selected: NoteWatch[]; disabled: boolean; onChange: (items: NoteWatch[]) => void }) {
  const [query, setQuery] = useState("");
  const ids = new Set(selected.map(item => item.id));
  const matches = options.filter(item => !ids.has(item.id) && `${item.name} ${item.detail ?? ""} ${item.id}`.toLowerCase().includes(query.toLowerCase()));
  return <section className="space-y-3" aria-label={`迁移${label}选择`}><div className="flex justify-between gap-3"><h3 className="text-sm font-medium">选择当前{label}</h3><span className="text-xs text-muted-foreground">{selected.length} / 256</span></div>
    {selected.length > 0 && <ul className="max-h-48 space-y-2 overflow-auto rounded-lg border p-2">{selected.map(item => {
      const current = options.find(option => watchKey(option) === watchKey(item));
      return <li key={watchKey(item)} className="flex items-start gap-2 rounded bg-muted/40 p-2"><div className="min-w-0 flex-1"><p className="break-words text-sm">{current?.name ?? item.name ?? item.id}</p>{!current && <p className="text-xs text-amber-700 dark:text-amber-300">对象身份已变化，请移除后重新选择当前对象。</p>}</div><Button type="button" size="icon" variant="ghost" disabled={disabled} aria-label={`移除迁移${label} ${current?.name ?? item.name ?? item.id}`} onClick={() => onChange(selected.filter(value => watchKey(value) !== watchKey(item)))}><X className="size-4" /></Button></li>;
    })}</ul>}
    <Input aria-label={`搜索迁移可选${label}`} placeholder={`搜索当前${label}`} value={query} disabled={disabled} onChange={e => setQuery(e.target.value)} />
    <div className="max-h-44 space-y-1 overflow-auto rounded-lg border p-2">{matches.slice(0, 40).map(option => <button key={watchKey(option)} type="button" className="flex w-full items-start justify-between gap-3 rounded p-2 text-left hover:bg-muted disabled:opacity-40" disabled={disabled || selected.length >= 256} onClick={() => onChange([...selected, { id: option.id, createdAt: option.createdAt, name: option.name }])}><span className="min-w-0"><span className="block break-words text-sm">{option.name}</span>{option.detail && <span className="text-xs text-muted-foreground">{option.detail}</span>}</span><span className="shrink-0 text-xs text-primary">选择</span></button>)}{matches.length === 0 && <p className="p-2 text-xs text-muted-foreground">没有匹配的当前对象。</p>}</div>
    {matches.length > 40 && <p className="text-xs text-muted-foreground">显示前 40 个匹配对象，请缩小搜索范围。</p>}
  </section>;
}
