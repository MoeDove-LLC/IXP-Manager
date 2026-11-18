import { LoaderCircle, Inbox, AlertCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
export function Loading() {
  return (
    <div
      className="flex items-center justify-center gap-3 py-20 text-sm text-muted-foreground"
      role="status"
    >
      <LoaderCircle className="size-5 animate-spin" />
      正在加载…
    </div>
  );
}
export function ErrorNotice({ message }: { message: string }) {
  return message ? (
    <div role="alert" className="error flex items-start gap-2">
      <AlertCircle className="mt-0.5 size-4 shrink-0" />
      {message}
    </div>
  ) : null;
}
export function Empty({
  title = "还没有数据",
  description,
  action,
  onAction,
}: {
  title?: string;
  description: string;
  action?: string;
  onAction?: () => void;
}) {
  return (
    <div className="empty">
      <div className="empty-icon">
        <Inbox className="size-6" />
      </div>
      <h3 className="text-base font-semibold">{title}</h3>
      <p className="mx-auto mt-2 mb-5 max-w-md text-sm leading-6 text-muted-foreground">
        {description}
      </p>
      {action && (
        <Button size="sm" onClick={onAction}>
          {action}
        </Button>
      )}
    </div>
  );
}
export function Heading({
  title,
  description,
  children,
  className = "",
}: {
  className?: string;
  title: string;
  description: string;
  children?: React.ReactNode;
}) {
  return (
    <div className={`page-heading ${className}`}>
      <div>
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      {children}
    </div>
  );
}
export const time = (value: unknown) =>
  typeof value === "string" &&
  !value.startsWith("0001-") &&
  !Number.isNaN(Date.parse(value))
    ? new Date(value).toLocaleString("zh-CN", { hour12: false })
    : "—";
export const message = (e: unknown) =>
  e instanceof Error ? e.message : "操作失败，请稍后重试。";
