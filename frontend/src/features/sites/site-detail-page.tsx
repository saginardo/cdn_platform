import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  KeyRound,
  LoaderCircle,
  Network,
  Plus,
  RefreshCw,
  Rocket,
  Save,
  ShieldCheck,
  Trash2,
  X,
} from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";

import { ConfirmDialog } from "@/components/confirm-dialog";
import { CopyButton } from "@/components/copy-button";
import { ListPagination } from "@/components/list-pagination";
import {
  EmptyState,
  PageBody,
  PageError,
  PageHeader,
  PageLoading,
} from "@/components/page";
import { StatusBadge } from "@/components/status-badge";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage } from "@/lib/api";
import { useListPagination } from "@/hooks/use-list-pagination";
import type {
  DeploymentTask,
  Node,
  PublishStatus,
  Settings,
  Site,
  TCPForward,
} from "@/lib/types";

interface SiteDraft {
  name: string;
  zone_id: string;
  domains: string;
  node_ids: string[];
  primary_url: string;
  primary_host: string;
  primary_sni: string;
  backup_enabled: boolean;
  backup_url: string;
  backup_host: string;
  backup_sni: string;
  passthrough: boolean;
  client_max_body_size_mb: number;
  read_write_timeout_seconds: number;
  inherit_dns_ttl: boolean;
  dns_ttl_seconds: number;
  tcp_only: boolean;
  tcp_forwards: TCPForward[];
  enabled: boolean;
}

interface TLSStatus {
  certificate_task: DeploymentTask | null;
  published_after_certificate: boolean;
}
interface Allowlist {
  site_id: string;
  ipv4_cidrs: string[];
  note: string;
}

export function SiteDetailPage() {
  const { siteId } = useParams();
  const isNew = !siteId;
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const sites = useQuery({
    queryKey: ["sites"],
    queryFn: () => api<Site[]>("/api/sites"),
  });
  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: () => api<Node[]>("/api/nodes"),
  });
  const settings = useQuery({
    queryKey: ["settings"],
    queryFn: () => api<Settings>("/api/settings"),
  });
  const site = sites.data?.find((item) => item.id === siteId);
  const [draft, setDraft] = useState<SiteDraft>(() => emptyDraft(60));
  const [baseline, setBaseline] = useState("");
  const [loadedKey, setLoadedKey] = useState("");
  const [discardOpen, setDiscardOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [allowlistOpen, setAllowlistOpen] = useState(false);
  const globalTTL = settings.data?.dns.default_ttl_seconds ?? 60;
  const dirty = Boolean(baseline && JSON.stringify(draft) !== baseline);
  const encodedID = encodeURIComponent(siteId ?? "");

  useEffect(() => {
    const key = isNew ? "new" : site?.id;
    if (!key || key === loadedKey) return;
    const next = site ? draftFromSite(site, globalTTL) : emptyDraft(globalTTL);
    setDraft(next);
    setBaseline(JSON.stringify(next));
    setLoadedKey(key);
  }, [globalTTL, isNew, loadedKey, site]);

  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const tls = useQuery({
    queryKey: ["site-tls", siteId],
    queryFn: () => api<TLSStatus>(`/api/sites/${encodedID}/tls-status`),
    enabled: !isNew && Boolean(site),
    refetchInterval: (query) =>
      activeTask(query.state.data?.certificate_task) ? 2_000 : 20_000,
  });
  const publish = useQuery({
    queryKey: ["site-publish", siteId],
    queryFn: () => api<PublishStatus>(`/api/sites/${encodedID}/publish-status`),
    enabled: !isNew && Boolean(site),
    refetchInterval: (query) =>
      activeTask(query.state.data?.task) ? 2_000 : 20_000,
  });
  const deletion = useQuery({
    queryKey: ["site-deletion", siteId],
    queryFn: () => api<PublishStatus>(`/api/sites/${encodedID}/delete-status`),
    enabled: Boolean(site?.deleting),
    refetchInterval: (query) =>
      activeTask(query.state.data?.task) ? 2_000 : false,
  });
  const allowlist = useQuery({
    queryKey: ["site-allowlist", siteId],
    queryFn: () => api<Allowlist>(`/api/sites/${encodedID}/origin-allowlist`),
    enabled: allowlistOpen && !isNew,
  });

  useEffect(() => {
    if (deletion.data?.task?.status !== "succeeded") return;
    toast.success("站点已安全删除");
    void queryClient.invalidateQueries({ queryKey: ["sites"] });
    navigate("/sites", { replace: true });
  }, [deletion.data?.task?.status, navigate, queryClient]);

  const save = useMutation({
    mutationFn: () =>
      api<Site>(isNew ? "/api/sites" : `/api/sites/${encodedID}`, {
        method: isNew ? "POST" : "PUT",
        body: JSON.stringify(sitePayload(draft)),
      }),
    onSuccess: (saved) => {
      const next = draftFromSite(saved, globalTTL);
      setDraft(next);
      setBaseline(JSON.stringify(next));
      setLoadedKey(saved.id);
      void queryClient.invalidateQueries({ queryKey: ["sites"] });
      toast.success(isNew ? "站点已创建" : "站点配置已保存");
      if (isNew)
        navigate(`/sites/${encodeURIComponent(saved.id)}`, { replace: true });
    },
    onError: (error) => toast.error(errorMessage(error)),
  });
  const operation = useMutation({
    mutationFn: ({ path }: { path: string }) =>
      api<DeploymentTask>(path, { method: "POST" }),
    onSuccess: (_, input) => {
      toast.success(
        input.path.endsWith("certificate")
          ? "TLS 签发已排队"
          : input.path.endsWith("invalidate-cache")
            ? "缓存失效已发布"
            : "站点发布已启动",
      );
      void queryClient.invalidateQueries({ queryKey: ["site-tls", siteId] });
      void queryClient.invalidateQueries({
        queryKey: ["site-publish", siteId],
      });
      void queryClient.invalidateQueries({ queryKey: ["sites"] });
    },
    onError: (error) => toast.error(errorMessage(error)),
  });
  const deleteSite = useMutation({
    mutationFn: () =>
      api<PublishStatus>(`/api/sites/${encodedID}`, {
        method: "DELETE",
        body: JSON.stringify({ confirmation: site?.name }),
      }),
    onSuccess: (status) => {
      queryClient.setQueryData(["site-deletion", siteId], status);
      void queryClient.invalidateQueries({ queryKey: ["sites"] });
      toast.success("安全删除已启动");
      setDeleteOpen(false);
    },
    onError: (error) => toast.error(errorMessage(error)),
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    save.mutate();
  }
  function goBack() {
    if (dirty) setDiscardOpen(true);
    else navigate("/sites");
  }
  const loading =
    sites.isLoading || nodes.isLoading || settings.isLoading || !loadedKey;

  return (
    <>
      <PageHeader
        title={isNew ? "添加站点" : (site?.name ?? "站点配置")}
        description={
          site
            ? `${site.domains.join(", ") || "无 HTTP 域名"} · ${site.id}`
            : "创建新的边缘站点配置"
        }
        actions={
          <Button variant="outline" onClick={goBack}>
            <ArrowLeft />
            返回站点
          </Button>
        }
      />
      <PageBody>
        {loading ? <PageLoading /> : null}
        {sites.error || nodes.error || settings.error ? (
          <PageError error={sites.error || nodes.error || settings.error} />
        ) : null}
        {!isNew && sites.data && !site ? (
          <EmptyState title="未找到站点" description="该站点可能已被删除" />
        ) : null}
        {!loading && (isNew || site) ? (
          <form
            className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_22rem]"
            onSubmit={submit}
          >
            <div className="space-y-5">
              {site?.deleting ? (
                <Alert variant="destructive">
                  <AlertTitle>站点正在删除</AlertTitle>
                  <AlertDescription>
                    {deletion.data?.task?.detail ||
                      "配置已锁定，正在撤销 DNS 并等待边缘节点确认。"}
                  </AlertDescription>
                </Alert>
              ) : null}
              <BasicSettings
                draft={draft}
                setDraft={setDraft}
                zoneLocked={!isNew}
              />
              <TrafficSettings draft={draft} setDraft={setDraft} />
              <NodeSelector
                nodes={nodes.data ?? []}
                selected={draft.node_ids}
                onChange={(node_ids) => setDraft({ ...draft, node_ids })}
              />
              <TCPForwards draft={draft} setDraft={setDraft} />
            </div>
            <aside className="space-y-4 xl:sticky xl:top-16 xl:self-start">
              <Card>
                <CardHeader>
                  <CardTitle>配置摘要</CardTitle>
                  <CardDescription>
                    {dirty ? "有未保存的更改" : "配置已同步"}
                  </CardDescription>
                </CardHeader>
                <CardContent className="grid gap-3 text-sm">
                  <Fact
                    label="流量模式"
                    value={
                      draft.tcp_only
                        ? "仅 TCP / TLS"
                        : draft.tcp_forwards.length
                          ? "HTTP + TCP"
                          : "HTTP / gRPC / WS"
                    }
                  />
                  <Fact
                    label="边缘节点"
                    value={`${draft.node_ids.length} 个`}
                  />
                  <Fact
                    label="DNS TTL"
                    value={
                      draft.inherit_dns_ttl
                        ? `${globalTTL} 秒（全局）`
                        : `${draft.dns_ttl_seconds} 秒`
                    }
                  />
                  <Fact
                    label="TCP 端口"
                    value={
                      draft.tcp_forwards.length
                        ? draft.tcp_forwards
                            .map((item) => item.listen_port || "--")
                            .join(", ")
                        : "未配置"
                    }
                  />
                  <Button
                    type="submit"
                    disabled={save.isPending || site?.deleting}
                  >
                    {save.isPending ? (
                      <LoaderCircle className="animate-spin" />
                    ) : (
                      <Save />
                    )}
                    {isNew ? "创建站点" : "保存更改"}
                  </Button>
                </CardContent>
              </Card>
              {site ? (
                <SiteOperations
                  site={site}
                  tls={tls.data}
                  publish={publish.data}
                  deletion={deletion.data}
                  pending={operation.isPending}
                  onPublish={() =>
                    operation.mutate({
                      path: `/api/sites/${encodedID}/publish`,
                    })
                  }
                  onCertificate={() =>
                    operation.mutate({
                      path: `/api/sites/${encodedID}/certificate`,
                    })
                  }
                  onInvalidate={() =>
                    operation.mutate({
                      path: `/api/sites/${encodedID}/invalidate-cache`,
                    })
                  }
                  onAllowlist={() => setAllowlistOpen(true)}
                  onDelete={() => setDeleteOpen(true)}
                />
              ) : null}
            </aside>
          </form>
        ) : null}
      </PageBody>
      <ConfirmDialog
        open={discardOpen}
        onOpenChange={setDiscardOpen}
        title="放弃未保存的更改？"
        description="当前站点表单包含未保存内容。"
        confirmLabel="放弃更改"
        destructive
        onConfirm={() => navigate("/sites")}
      />
      <ConfirmDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title={site?.deleting ? "重试安全删除" : "安全删除站点"}
        description="控制面会撤销托管 DNS、从所有边缘节点移除配置，并清理证书材料。"
        confirmation={site?.name}
        confirmLabel={site?.deleting ? "重试删除" : "开始删除"}
        destructive
        busy={deleteSite.isPending}
        onConfirm={async () => {
          await deleteSite.mutateAsync();
        }}
      />
      <AllowlistDialog
        open={allowlistOpen}
        onOpenChange={setAllowlistOpen}
        data={allowlist.data}
        loading={allowlist.isLoading}
      />
    </>
  );
}

function BasicSettings({
  draft,
  setDraft,
  zoneLocked,
}: {
  draft: SiteDraft;
  setDraft: (draft: SiteDraft) => void;
  zoneLocked: boolean;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>基本配置</CardTitle>
        <CardDescription>名称、Cloudflare 区域与入口域名</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 sm:grid-cols-2">
        <Field label="站点名称" id="site-name">
          <Input
            id="site-name"
            required
            maxLength={100}
            value={draft.name}
            onChange={(event) =>
              setDraft({ ...draft, name: event.target.value })
            }
          />
        </Field>
        <Field label="Cloudflare Zone ID" id="site-zone">
          <Input
            id="site-zone"
            required
            disabled={zoneLocked}
            value={draft.zone_id}
            onChange={(event) =>
              setDraft({ ...draft, zone_id: event.target.value })
            }
          />
        </Field>
        <div className="grid gap-2 sm:col-span-2">
          <Label htmlFor="site-domains">域名</Label>
          <Textarea
            id="site-domains"
            rows={3}
            value={draft.domains}
            onChange={(event) =>
              setDraft({ ...draft, domains: event.target.value })
            }
            placeholder="cdn.example.com, static.example.com"
          />
          <p className="text-xs text-muted-foreground">使用逗号或换行分隔</p>
        </div>
        <div className="flex items-center justify-between sm:col-span-2">
          <div>
            <Label htmlFor="site-enabled">启用站点</Label>
            <p className="text-xs text-muted-foreground">
              停用后下次发布会撤销入口服务
            </p>
          </div>
          <Switch
            id="site-enabled"
            checked={draft.enabled}
            onCheckedChange={(enabled) => setDraft({ ...draft, enabled })}
          />
        </div>
      </CardContent>
    </Card>
  );
}

function TrafficSettings({
  draft,
  setDraft,
}: {
  draft: SiteDraft;
  setDraft: (draft: SiteDraft) => void;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>流量与源站</CardTitle>
        <CardDescription>HTTP 系列协议或纯 TCP/TLS 转发</CardDescription>
      </CardHeader>
      <CardContent>
        <Tabs
          value={draft.tcp_only ? "tcp" : "http"}
          onValueChange={(value) =>
            setDraft({ ...draft, tcp_only: value === "tcp" })
          }
        >
          <TabsList>
            <TabsTrigger value="http">HTTP / gRPC / WS</TabsTrigger>
            <TabsTrigger value="tcp">仅 TCP / TLS</TabsTrigger>
          </TabsList>
          <TabsContent value="http" className="mt-5 space-y-5">
            <OriginFields
              title="主源站"
              required
              url={draft.primary_url}
              host={draft.primary_host}
              sni={draft.primary_sni}
              onChange={(values) =>
                setDraft({
                  ...draft,
                  primary_url: values.url,
                  primary_host: values.host,
                  primary_sni: values.sni,
                })
              }
            />
            <Separator />
            <div className="flex items-center justify-between">
              <div>
                <Label htmlFor="backup-origin">备用源站</Label>
                <p className="text-xs text-muted-foreground">
                  主源站不可用时回退
                </p>
              </div>
              <Switch
                id="backup-origin"
                checked={draft.backup_enabled}
                onCheckedChange={(backup_enabled) =>
                  setDraft({ ...draft, backup_enabled })
                }
              />
            </div>
            {draft.backup_enabled ? (
              <OriginFields
                title="备用源站"
                url={draft.backup_url}
                host={draft.backup_host}
                sni={draft.backup_sni}
                onChange={(values) =>
                  setDraft({
                    ...draft,
                    backup_url: values.url,
                    backup_host: values.host,
                    backup_sni: values.sni,
                  })
                }
              />
            ) : null}
            <Separator />
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="最大请求体（MiB）" id="body-size">
                <Input
                  id="body-size"
                  type="number"
                  min={1}
                  max={1024}
                  required
                  value={draft.client_max_body_size_mb}
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      client_max_body_size_mb: Number(event.target.value),
                    })
                  }
                />
              </Field>
              <Field label="读写超时（秒）" id="rw-timeout">
                <Input
                  id="rw-timeout"
                  type="number"
                  min={1}
                  required
                  value={draft.read_write_timeout_seconds}
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      read_write_timeout_seconds: Number(event.target.value),
                    })
                  }
                />
              </Field>
            </div>
            <div className="flex items-center justify-between">
              <div>
                <Label htmlFor="passthrough">回源直通</Label>
                <p className="text-xs text-muted-foreground">
                  关闭缓存并直接代理到源站
                </p>
              </div>
              <Switch
                id="passthrough"
                checked={draft.passthrough}
                onCheckedChange={(passthrough) =>
                  setDraft({ ...draft, passthrough })
                }
              />
            </div>
            <Separator />
            <div className="grid gap-4 sm:grid-cols-[1fr_10rem]">
              <div className="flex items-center justify-between">
                <div>
                  <Label htmlFor="inherit-ttl">继承全局 DNS TTL</Label>
                  <p className="text-xs text-muted-foreground">
                    范围 60–300 秒
                  </p>
                </div>
                <Switch
                  id="inherit-ttl"
                  checked={draft.inherit_dns_ttl}
                  onCheckedChange={(inherit_dns_ttl) =>
                    setDraft({ ...draft, inherit_dns_ttl })
                  }
                />
              </div>
              <Input
                type="number"
                min={60}
                max={300}
                disabled={draft.inherit_dns_ttl}
                value={draft.dns_ttl_seconds}
                onChange={(event) =>
                  setDraft({
                    ...draft,
                    dns_ttl_seconds: Number(event.target.value),
                  })
                }
                aria-label="站点 DNS TTL"
              />
            </div>
          </TabsContent>
          <TabsContent value="tcp" className="mt-5">
            <Alert>
              <Network />
              <AlertTitle>纯 TCP/TLS 模式</AlertTitle>
              <AlertDescription>
                此模式不创建 HTTP 入口，请至少在下方配置一个 TCP 转发端口。
              </AlertDescription>
            </Alert>
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  );
}

function OriginFields({
  title,
  required = false,
  url,
  host,
  sni,
  onChange,
}: {
  title: string;
  required?: boolean;
  url: string;
  host: string;
  sni: string;
  onChange: (values: { url: string; host: string; sni: string }) => void;
}) {
  const tls = /^(https|wss|grpcs):/i.test(url);
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <div className="sm:col-span-2 text-sm font-medium">{title}</div>
      <Field label="源站 URL" id={`${title}-url`}>
        <Input
          id={`${title}-url`}
          required={required}
          value={url}
          onChange={(event) => onChange({ url: event.target.value, host, sni })}
          placeholder="https://origin.example.com:443"
        />
      </Field>
      <Field label="Host Header" id={`${title}-host`}>
        <Input
          id={`${title}-host`}
          value={host}
          onChange={(event) => onChange({ url, host: event.target.value, sni })}
          placeholder="origin.example.com"
        />
      </Field>
      {tls ? (
        <div className="grid gap-2 sm:col-span-2">
          <Label htmlFor={`${title}-sni`}>回源 TLS SNI</Label>
          <Input
            id={`${title}-sni`}
            value={sni}
            onChange={(event) =>
              onChange({ url, host, sni: event.target.value })
            }
            placeholder="origin.example.com"
          />
        </div>
      ) : null}
    </div>
  );
}

function NodeSelector({
  nodes,
  selected,
  onChange,
}: {
  nodes: Node[];
  selected: string[];
  onChange: (selected: string[]) => void;
}) {
  const available = nodes.filter(
    (node) => !["revoked", "uninstalling", "uninstalled"].includes(node.status),
  );
  const pagination = useListPagination(available);
  return (
    <Card>
      <CardHeader>
        <CardTitle>边缘节点</CardTitle>
        <CardDescription>选择承载此站点的节点</CardDescription>
      </CardHeader>
      <CardContent>
        {available.length ? (
          <>
            <div className="grid gap-2 sm:grid-cols-2">
              {pagination.items.map((node) => {
                const checked = selected.includes(node.id);
                return (
                  <label
                    key={node.id}
                    className="flex items-center gap-3 border px-3 py-3 text-sm hover:bg-muted/30"
                  >
                    <Checkbox
                      checked={checked}
                      onCheckedChange={(value) =>
                        onChange(
                          value
                            ? [...selected, node.id]
                            : selected.filter((id) => id !== node.id),
                        )
                      }
                    />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate font-medium">
                        {node.name}
                      </span>
                      <span className="block font-mono text-xs text-muted-foreground">
                        {node.public_ipv4}
                      </span>
                    </span>
                    <StatusBadge status={node.status} />
                  </label>
                );
              })}
            </div>
            <ListPagination
              pagination={pagination}
              itemLabel="个节点"
              className="mt-3 border"
            />
          </>
        ) : (
          <EmptyState
            title="没有可用节点"
            description="先添加边缘节点或恢复节点授权"
          />
        )}
      </CardContent>
    </Card>
  );
}

function TCPForwards({
  draft,
  setDraft,
}: {
  draft: SiteDraft;
  setDraft: (draft: SiteDraft) => void;
}) {
  const pagination = useListPagination(draft.tcp_forwards);
  const update = (index: number, values: Partial<TCPForward>) =>
    setDraft({
      ...draft,
      tcp_forwards: draft.tcp_forwards.map((item, current) =>
        current === index ? { ...item, ...values } : item,
      ),
    });
  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between gap-4">
        <div>
          <CardTitle>TCP / TLS 转发</CardTitle>
          <CardDescription>
            可与 HTTP 入口同时使用，最多 32 个端口
          </CardDescription>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={draft.tcp_forwards.length >= 32}
          onClick={() =>
            setDraft({
              ...draft,
              tcp_forwards: [...draft.tcp_forwards, emptyForward()],
            })
          }
        >
          <Plus />
          添加端口
        </Button>
      </CardHeader>
      <CardContent className="space-y-3">
        {draft.tcp_forwards.length ? (
          <>
            {pagination.items.map((forward, pageIndex) => {
              const index = pagination.startIndex + pageIndex;
              return (
                <div key={index} className="relative border p-4">
                  <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
                    <Field label="名称" id={`tcp-name-${index}`}>
                      <Input
                        id={`tcp-name-${index}`}
                        required
                        maxLength={100}
                        value={forward.name}
                        onChange={(event) =>
                          update(index, { name: event.target.value })
                        }
                        placeholder="IMAPS"
                      />
                    </Field>
                    <Field label="监听端口" id={`tcp-listen-${index}`}>
                      <Input
                        id={`tcp-listen-${index}`}
                        required
                        type="number"
                        min={1}
                        max={65535}
                        value={forward.listen_port || ""}
                        onChange={(event) =>
                          update(index, {
                            listen_port: Number(event.target.value),
                          })
                        }
                      />
                    </Field>
                    <Field label="上游主机" id={`tcp-host-${index}`}>
                      <Input
                        id={`tcp-host-${index}`}
                        required
                        value={forward.upstream_host}
                        onChange={(event) =>
                          update(index, { upstream_host: event.target.value })
                        }
                      />
                    </Field>
                    <Field label="上游端口" id={`tcp-upstream-${index}`}>
                      <Input
                        id={`tcp-upstream-${index}`}
                        required
                        type="number"
                        min={1}
                        max={65535}
                        value={forward.upstream_port || ""}
                        onChange={(event) =>
                          update(index, {
                            upstream_port: Number(event.target.value),
                          })
                        }
                      />
                    </Field>
                    <SelectField
                      label="连接超时"
                      value={String(forward.connect_timeout_seconds)}
                      onChange={(value) =>
                        update(index, {
                          connect_timeout_seconds: Number(value),
                        })
                      }
                      options={[
                        ["5", "5 秒"],
                        ["10", "10 秒"],
                        ["30", "30 秒"],
                        ["60", "60 秒"],
                      ]}
                    />
                    <SelectField
                      label="空闲超时"
                      value={String(forward.idle_timeout_seconds)}
                      onChange={(value) =>
                        update(index, { idle_timeout_seconds: Number(value) })
                      }
                      options={[
                        ["300", "5 分钟"],
                        ["900", "15 分钟"],
                        ["1800", "30 分钟"],
                        ["3600", "60 分钟"],
                      ]}
                    />
                    <Toggle
                      label="入口 TLS"
                      checked={forward.listen_tls}
                      onChange={(listen_tls) => update(index, { listen_tls })}
                    />
                    <Toggle
                      label="上游 TLS"
                      checked={forward.upstream_tls}
                      onChange={(upstream_tls) =>
                        update(index, { upstream_tls })
                      }
                    />
                    {forward.upstream_tls ? (
                      <div className="grid gap-2 sm:col-span-2 xl:col-span-4">
                        <Label htmlFor={`tcp-sni-${index}`}>上游 TLS SNI</Label>
                        <Input
                          id={`tcp-sni-${index}`}
                          value={forward.upstream_tls_server_name || ""}
                          onChange={(event) =>
                            update(index, {
                              upstream_tls_server_name: event.target.value,
                            })
                          }
                        />
                      </div>
                    ) : null}
                  </div>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    className="absolute right-2 top-2"
                    aria-label="删除 TCP 转发"
                    onClick={() =>
                      setDraft({
                        ...draft,
                        tcp_forwards: draft.tcp_forwards.filter(
                          (_, current) => current !== index,
                        ),
                      })
                    }
                  >
                    <X />
                  </Button>
                </div>
              );
            })}
            <ListPagination
              pagination={pagination}
              itemLabel="个转发端口"
              className="border"
            />
          </>
        ) : (
          <EmptyState
            title="未配置 TCP 转发"
            description={
              draft.tcp_only
                ? "纯 TCP 模式至少需要一个监听端口"
                : "可选：为站点增加四层转发端口"
            }
          />
        )}
      </CardContent>
    </Card>
  );
}

function SiteOperations({
  site,
  tls,
  publish,
  deletion,
  pending,
  onPublish,
  onCertificate,
  onInvalidate,
  onAllowlist,
  onDelete,
}: {
  site: Site;
  tls?: TLSStatus;
  publish?: PublishStatus;
  deletion?: PublishStatus;
  pending: boolean;
  onPublish: () => void;
  onCertificate: () => void;
  onInvalidate: () => void;
  onAllowlist: () => void;
  onDelete: () => void;
}) {
  const publishActive = activeTask(publish?.task);
  const certActive = activeTask(tls?.certificate_task);
  const publishPagination = useListPagination(publish?.nodes ?? []);
  const cacheable =
    !site.tcp_only &&
    !site.passthrough &&
    /^https?:/i.test(site.primary_origin.url);
  const needsTLS =
    site.domains.length > 0 ||
    site.tcp_forwards.some((forward) => forward.listen_tls);
  return (
    <Card>
      <CardHeader>
        <CardTitle>发布与运维</CardTitle>
        <CardDescription>配置保存后需发布到边缘节点</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <OperationState
          label="发布"
          task={site.deleting ? deletion?.task : publish?.task}
          fallback={site.published ? "已发布" : "尚未发布"}
        />
        {needsTLS ? (
          <OperationState
            label="TLS"
            task={tls?.certificate_task}
            fallback="尚未签发"
            extra={tls?.published_after_certificate ? "已部署" : undefined}
          />
        ) : null}
        <Button
          type="button"
          disabled={site.deleting || pending || publishActive}
          onClick={onPublish}
        >
          <Rocket />
          {site.published ? "重新发布" : "发布站点"}
        </Button>
        {needsTLS ? (
          <Button
            type="button"
            variant="outline"
            disabled={site.deleting || pending || certActive}
            onClick={onCertificate}
          >
            <KeyRound />
            签发 TLS
          </Button>
        ) : null}
        {cacheable ? (
          <Button
            type="button"
            variant="outline"
            disabled={site.deleting || pending}
            onClick={onInvalidate}
          >
            <RefreshCw />
            全量缓存失效
          </Button>
        ) : null}
        <Button
          type="button"
          variant="outline"
          disabled={site.deleting}
          onClick={onAllowlist}
        >
          <ShieldCheck />
          源站白名单
        </Button>
        {publish?.nodes?.length ? (
          <div className="border">
            <div className="max-h-44 overflow-auto">
              <Table>
                <TableBody>
                  {publishPagination.items.map((node) => (
                    <TableRow key={node.node_id}>
                      <TableCell className="text-xs">
                        {node.node_name || node.node_id}
                      </TableCell>
                      <TableCell className="text-right">
                        <StatusBadge status={node.status} />
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
            <ListPagination pagination={publishPagination} itemLabel="个节点" />
          </div>
        ) : null}
        <Separator />
        <Button
          type="button"
          variant="destructive"
          disabled={pending || publishActive || certActive}
          onClick={onDelete}
        >
          <Trash2 />
          {site.deleting ? "查看/重试删除" : "删除站点"}
        </Button>
      </CardContent>
    </Card>
  );
}

function AllowlistDialog({
  open,
  onOpenChange,
  data,
  loading,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  data?: Allowlist;
  loading: boolean;
}) {
  const pagination = useListPagination(data?.ipv4_cidrs ?? []);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>源站防火墙白名单</DialogTitle>
          <DialogDescription>
            允许当前草稿和已发布节点的 IPv4 CIDR
          </DialogDescription>
        </DialogHeader>
        {loading ? (
          <div className="py-8 text-center text-sm text-muted-foreground">
            正在加载...
          </div>
        ) : (
          <div className="space-y-3">
            {data?.ipv4_cidrs.length ? (
              <>
                {pagination.items.map((cidr) => (
                  <div
                    key={cidr}
                    className="flex items-center gap-2 border px-3 py-2"
                  >
                    <code className="min-w-0 flex-1 text-sm">{cidr}</code>
                    <CopyButton value={cidr} />
                  </div>
                ))}
                <ListPagination
                  pagination={pagination}
                  itemLabel="个地址"
                  className="border"
                />
              </>
            ) : (
              <EmptyState title="暂无可用地址" />
            )}
            <p className="text-xs leading-5 text-muted-foreground">
              {data?.note}
            </p>
          </div>
        )}
        <DialogFooter>
          <Button onClick={() => onOpenChange(false)}>完成</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function OperationState({
  label,
  task,
  fallback,
  extra,
}: {
  label: string;
  task?: DeploymentTask | null;
  fallback: string;
  extra?: string;
}) {
  return (
    <div className="flex items-start justify-between gap-3 border px-3 py-2 text-sm">
      <div>
        <span>{label}</span>
        {task?.detail ? (
          <p className="mt-1 text-xs text-muted-foreground">{task.detail}</p>
        ) : null}
      </div>
      {task ? (
        <StatusBadge status={task.status} />
      ) : (
        <span className="text-xs text-muted-foreground">
          {extra || fallback}
        </span>
      )}
    </div>
  );
}
function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-right font-medium">{value}</span>
    </div>
  );
}
function Field({
  label,
  id,
  children,
}: {
  label: string;
  id: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{label}</Label>
      {children}
    </div>
  );
}
function SelectField({
  label,
  value,
  onChange,
  options,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  options: string[][];
}) {
  return (
    <div className="grid gap-2">
      <Label>{label}</Label>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map(([key, text]) => (
            <SelectItem key={key} value={key}>
              {text}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}
function Toggle({
  label,
  checked,
  onChange,
}: {
  label: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <div className="flex items-center justify-between border px-3 py-2">
      <Label>{label}</Label>
      <Switch checked={checked} onCheckedChange={onChange} />
    </div>
  );
}
function activeTask(task?: DeploymentTask | null) {
  return Boolean(
    task && ["queued", "dispatching", "applying"].includes(task.status),
  );
}

function emptyDraft(ttl: number): SiteDraft {
  return {
    name: "",
    zone_id: "",
    domains: "",
    node_ids: [],
    primary_url: "https://",
    primary_host: "",
    primary_sni: "",
    backup_enabled: false,
    backup_url: "",
    backup_host: "",
    backup_sni: "",
    passthrough: false,
    client_max_body_size_mb: 128,
    read_write_timeout_seconds: 360,
    inherit_dns_ttl: true,
    dns_ttl_seconds: ttl,
    tcp_only: false,
    tcp_forwards: [],
    enabled: true,
  };
}
function emptyForward(): TCPForward {
  return {
    name: "",
    listen_port: 0,
    listen_tls: true,
    upstream_host: "",
    upstream_port: 0,
    upstream_tls: true,
    upstream_tls_server_name: "",
    connect_timeout_seconds: 10,
    idle_timeout_seconds: 300,
  };
}
function draftFromSite(site: Site, ttl: number): SiteDraft {
  return {
    name: site.name,
    zone_id: site.zone_id,
    domains: site.domains.join(", "),
    node_ids: [...site.node_ids],
    primary_url: site.primary_origin.url,
    primary_host: site.primary_origin.host_header || "",
    primary_sni: site.primary_origin.tls_server_name || "",
    backup_enabled: Boolean(site.backup_origin),
    backup_url: site.backup_origin?.url || "",
    backup_host: site.backup_origin?.host_header || "",
    backup_sni: site.backup_origin?.tls_server_name || "",
    passthrough: site.passthrough,
    client_max_body_size_mb: site.client_max_body_size_mb ?? 128,
    read_write_timeout_seconds: site.read_write_timeout_seconds ?? 360,
    inherit_dns_ttl: site.dns_ttl_seconds == null,
    dns_ttl_seconds: site.dns_ttl_seconds ?? ttl,
    tcp_only: site.tcp_only,
    tcp_forwards: site.tcp_forwards.map((forward) => ({ ...forward })),
    enabled: site.enabled,
  };
}
function splitList(value: string) {
  return value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}
function sitePayload(draft: SiteDraft) {
  const payload: Record<string, unknown> = {
    name: draft.name,
    zone_id: draft.zone_id,
    domains: splitList(draft.domains),
    node_ids: draft.node_ids,
    primary_origin: {
      url: draft.primary_url,
      host_header: draft.primary_host,
      tls_server_name: /^(https|wss|grpcs):/i.test(draft.primary_url)
        ? draft.primary_sni
        : "",
      enabled: true,
    },
    passthrough: draft.passthrough,
    client_max_body_size_mb: draft.client_max_body_size_mb,
    read_write_timeout_seconds: draft.read_write_timeout_seconds,
    dns_ttl_seconds: draft.inherit_dns_ttl ? null : draft.dns_ttl_seconds,
    tcp_only: draft.tcp_only,
    tcp_forwards: draft.tcp_forwards,
    enabled: draft.enabled,
  };
  if (draft.backup_enabled && draft.backup_url.trim())
    payload.backup_origin = {
      url: draft.backup_url,
      host_header: draft.backup_host,
      tls_server_name: /^(https|wss|grpcs):/i.test(draft.backup_url)
        ? draft.backup_sni
        : "",
      enabled: true,
    };
  return payload;
}
