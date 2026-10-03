// 票05 · mock daemon——本地测试面（localhost 随机端口,测试自起自灭;不要求
// 活实例、不碰 ~/.dsh,D12 红线）。三口（/dsh/gate、/dsh/event、/dsh/handoff）
// 的对面替身：记录请求（路径/鉴权/体）＋按路径可编程回话;默认 200 {"ok":true}。
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";

export interface RecordedRequest {
  path: string;
  auth: string | null;
  contentType: string | null;
  body: unknown;
}

export interface MockResponse {
  status: number;
  json?: unknown;
}

export type RouteHandler = (rec: RecordedRequest) => MockResponse | Promise<MockResponse>;

export interface MockDaemon {
  url: string;
  requests: RecordedRequest[];
  route(path: string, handler: RouteHandler): void;
  unroute(path: string): void;
  requestsFor(path: string): RecordedRequest[];
  close(): Promise<void>;
}

export function startMockDaemon(): Promise<MockDaemon> {
  const requests: RecordedRequest[] = [];
  const routes = new Map<string, RouteHandler>();
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      const raw = Buffer.concat(chunks).toString("utf8");
      let body: unknown = null;
      try {
        body = JSON.parse(raw);
      } catch {
        body = raw;
      }
      const rec: RecordedRequest = {
        path: req.url ?? "",
        auth: req.headers.authorization ?? null,
        contentType: typeof req.headers["content-type"] === "string" ? req.headers["content-type"] : null,
        body,
      };
      requests.push(rec);
      const handler = routes.get(rec.path);
      Promise.resolve(handler ? handler(rec) : { status: 200, json: { ok: true } })
        .then((out) => {
          const payload = out.json === undefined ? "{}" : JSON.stringify(out.json);
          res.writeHead(out.status, { "Content-Type": "application/json" });
          res.end(payload);
        })
        .catch(() => {
          res.writeHead(500, { "Content-Type": "application/json" });
          res.end(JSON.stringify({ error: "mock handler threw" }));
        });
    });
  });
  return new Promise<MockDaemon>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const addr = server.address() as AddressInfo;
      const mock: MockDaemon = {
        url: `http://127.0.0.1:${addr.port}`,
        requests,
        route: (p, h) => void routes.set(p, h),
        unroute: (p) => void routes.delete(p),
        requestsFor: (p) => requests.filter((r) => r.path === p),
        close: () =>
          new Promise<void>((res2, rej2) => {
            // undici 连接池的 keep-alive 连接会让裸 close() 永等——先断全部连接
            server.closeAllConnections();
            server.close((e) => (e ? rej2(e) : res2()));
          }),
      };
      resolve(mock);
    });
  });
}

/** 一个已关闭端口的 URL——模拟 daemon 不可达（ECONNREFUSED,fail-open 面用）。 */
export async function closedDaemonURL(): Promise<{ url: string }> {
  const m = await startMockDaemon();
  const url = m.url;
  await m.close();
  return { url };
}

/** 轮询等待条件成立（fire-and-forget 上报落地的观测面）。 */
export async function waitUntil(cond: () => boolean, ms = 2000): Promise<void> {
  const t0 = Date.now();
  while (!cond()) {
    if (Date.now() - t0 > ms) throw new Error("waitUntil 超时");
    await new Promise((r) => setTimeout(r, 10));
  }
}
