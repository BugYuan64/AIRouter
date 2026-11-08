/** 控制面 API 客户端。只访问同源 /admin/api，不含 CPA 凭据。 */

export type Capability = 'supported' | 'unsupported' | 'unknown'
export type Role = 'admin' | 'client'

export interface ModelProfile {
  id: string
  ownedBy: string
  chat: Capability
  responses: Capability
  messages: Capability
  tools: Capability
  contextWindow: number | null
  verifiedAt: string | null
}

export interface GatewayStatus {
  version: string
  upstream: 'ok' | 'degraded' | 'down'
  gatewayRetries: number
  activePolicy: string | null
  catalogCount: number
  storage: string
}

export interface UsageRow {
  clientId: string
  protocol: string
  requests: number
  errors: number
  streams: number
  avgLatencyMs: number
}

export interface UsageDetail {
  id: number
  clientId: string
  model: string
  protocol: string
  status: number
  stream: boolean
  inputTokens: number | null
  outputTokens: number | null
  costMicros: number | null
  latencyMs: number
  firstByteMs: number | null
  createdAt: string
}

export interface UsageResponse {
  from: string
  to: string
  requests: number
  errors: number
  rows: UsageRow[]
  tokensKnown: boolean
  costKnown: boolean
}

export interface ClientAccount {
  id: string
  keyPrefix: string
  label: string
  isAdmin: boolean
  status: 'active' | 'revoked'
  models: string[]
  createdAt: string
  revokedAt: string | null
}

export interface Identity {
  role: Role
  clientId: string
  capabilities: Record<string, boolean>
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly type: string,
    message: string,
  ) {
    super(message)
  }
}

import { ref } from 'vue'

const BASE = '/admin/api'
const KEY_STORAGE = 'airouter.token'
const ROLE_STORAGE = 'airouter.role'

/** 密钥只存 sessionStorage：关闭标签页即清除，不写入长期存储。 */
let token = sessionStorage.getItem(KEY_STORAGE) ?? ''

// 用 ref 承载角色，否则 computed 无法追踪变化，侧栏不会随登录刷新。
const roleRef = ref<Role>((sessionStorage.getItem(ROLE_STORAGE) as Role | null) ?? 'client')
const listeners = new Set<() => void>()

export function setAdminKey(key: string, detectedRole: Role = 'client') {
  token = key
  roleRef.value = detectedRole
  if (key) {
    sessionStorage.setItem(KEY_STORAGE, key)
    sessionStorage.setItem(ROLE_STORAGE, detectedRole)
  } else {
    sessionStorage.removeItem(KEY_STORAGE)
    sessionStorage.removeItem(ROLE_STORAGE)
  }
  listeners.forEach((fn) => fn())
}

export function hasAdminKey() {
  return token !== ''
}

export function currentRole(): Role {
  return roleRef.value
}

/** 响应式角色，供组件直接使用。 */
export function useRole() {
  return roleRef
}

export function onPrincipalChange(fn: () => void) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

async function parseError(response: Response): Promise<ApiError> {
  let type = 'http_error'
  let message = `管理接口返回 ${response.status}`
  try {
    const body = (await response.json()) as { error?: { type?: string; message?: string } }
    if (body.error?.type) type = body.error.type
    if (body.error?.message) message = body.error.message
  } catch {
    // 非 JSON 响应保持默认文案，不把 HTML 塞进错误提示
  }
  return new ApiError(response.status, type, message)
}

async function request<T>(path: string): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (token) headers.Authorization = `Bearer ${token}`
  const response = await fetch(`${BASE}${path}`, { headers })
  if (!response.ok) throw await parseError(response)
  return (await response.json()) as T
}

async function send<T>(path: string, method: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const response = await fetch(`${BASE}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!response.ok) throw await parseError(response)
  return (await response.json()) as T
}

/**
 * 用给定密钥向服务端自省身份。
 * 角色来自服务端认证结果，前端不自行推断；前端也不以角色作为安全边界，
 * 真正的权限校验始终在服务端。
 */
export async function detectRole(key: string): Promise<Identity> {
  const response = await fetch(`${BASE}/whoami`, {
    headers: { Accept: 'application/json', Authorization: `Bearer ${key}` },
  })
  if (response.status === 401) {
    throw new ApiError(401, 'authentication_error', '密钥无效或已吊销')
  }
  if (!response.ok) throw await parseError(response)
  return (await response.json()) as Identity
}

export const api = {
  whoami: () => request<Identity>('/whoami'),
  status: () => request<GatewayStatus>('/status'),
  models: () => request<{ data: ModelProfile[]; stale?: boolean }>('/models'),
  usage: () => request<UsageResponse>('/usage'),
  usageRecent: (limit = 50) => request<{ data: UsageDetail[] }>(`/usage/recent?limit=${limit}`),
  keys: () => request<{ data: ClientAccount[] }>('/keys'),
  createKey: (input: { id: string; label: string; models: string[] }) =>
    send<{ data: ClientAccount; key: string; note: string }>('/keys', 'POST', input),
  revokeKey: (id: string) => send<{ revoked: string }>(`/keys/${encodeURIComponent(id)}`, 'DELETE'),
}
