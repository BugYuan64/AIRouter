<script setup lang="ts">
import { computed, ref } from 'vue'
import AppIcon from '../components/AppIcon'
import { currentRole } from '../api'

const role = currentRole()
const activeTab = ref<'quickstart' | 'endpoints' | 'errors'>('quickstart')

const baseUrl = computed(() => `${window.location.origin}`)

const chatExample = computed(() => `curl ${baseUrl.value}/v1/chat/completions \\
  -H "Authorization: Bearer $AIRouter_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "gpt-6.1-sol",
    "messages": [{"role": "user", "content": "Hello"}]
  }'`)

const messagesExample = computed(() => `curl ${baseUrl.value}/v1/messages \\
  -H "x-api-key: $AIRouter_KEY" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "gpt-6.1-sol",
    "max_tokens": 256,
    "messages": [{"role": "user", "content": "Hello"}]
  }'`)

const tabs = [
  { id: 'quickstart' as const, label: '快速开始', icon: 'lucide:rocket' },
  { id: 'endpoints' as const, label: '接口说明', icon: 'lucide:list' },
  { id: 'errors' as const, label: '错误处理', icon: 'lucide:triangle-alert' },
]

const endpoints = [
  { method: 'POST', path: '/v1/chat/completions', note: 'OpenAI 兼容，支持流式' },
  { method: 'POST', path: '/v1/responses', note: '完整输入随请求提交；不支持有状态引用' },
  { method: 'POST', path: '/v1/messages', note: 'Anthropic 原生，接受 Bearer 或 x-api-key' },
  { method: 'POST', path: '/v1/messages/count_tokens', note: 'token 计数' },
  { method: 'GET', path: '/v1/models', note: '只返回你被允许的模型' },
]

const errors = [
  { status: '401', type: 'authentication_error', note: 'Key 无效或已吊销' },
  { status: '403', type: 'permission_error', note: '该模型不在你的允许列表内' },
  { status: '400', type: 'invalid_request_error', note: 'JSON 不合法、包含重复字段或缺少 model' },
  { status: '413', type: 'invalid_request_error', note: '请求体超过大小上限' },
  { status: '501', type: 'invalid_request_error', note: '使用了未支持的有状态特性' },
  { status: '502', type: 'upstream_error', note: '上游不可用；网关不会重试' },
  { status: '504', type: 'upstream_error', note: '上游超时' },
]
</script>

<template>
  <div class="mx-auto max-w-[880px]">
    <header class="mb-8">
      <p class="eyebrow mb-3 flex items-center gap-2">
        <AppIcon name="lucide:book-open" :size="12" />
        使用文档
      </p>
      <h2 class="display-sm mb-3">把它当成一个模型端点。</h2>
      <p class="max-w-2xl text-sm leading-relaxed text-muted">
        AIRouter 保留模型 ID、原始 JSON 正文与原生协议。不做跨协议转换，不启用智能选模、
        网关重试或跨模型回退——失败就是失败，不会静默换模型。
      </p>
    </header>

    <div class="mb-8 flex gap-1 border-b border-line">
      <button
        v-for="tab in tabs"
        :key="tab.id"
        class="flex items-center gap-2 border-b-2 px-4 py-2.5 text-sm transition-colors"
        :class="activeTab === tab.id
          ? 'border-ink font-medium text-ink'
          : 'border-transparent text-muted hover:text-ink'"
        @click="activeTab = tab.id"
      >
        <AppIcon :name="tab.icon" :size="14" />
        {{ tab.label }}
      </button>
    </div>

    <section v-if="activeTab === 'quickstart'" class="space-y-8">
      <div>
        <h3 class="mb-3 text-sm font-medium">1. 拿到你的 Key</h3>
        <p class="text-sm leading-relaxed text-muted">
          联系管理员创建账户。Key 只在创建时显示一次，请立即保存——
          服务端只存摘要，无法找回。
        </p>
      </div>

      <div>
        <h3 class="mb-3 text-sm font-medium">2. 设置 Base URL</h3>
        <div class="surface-panel px-4 py-3">
          <code class="mono break-all text-xs">{{ baseUrl }}</code>
        </div>
        <p class="mt-2 text-xs text-muted">
          OpenAI 兼容客户端填 <span class="mono">{{ baseUrl }}/v1</span>；
          Anthropic 客户端直接填 origin，它会自己拼接端点。
        </p>
      </div>

      <div>
        <h3 class="mb-3 text-sm font-medium">3. 发起请求</h3>
        <p class="mb-2 text-xs text-muted">OpenAI 格式：</p>
        <pre class="surface-panel overflow-x-auto px-4 py-3"><code class="mono text-[11px] leading-relaxed">{{ chatExample }}</code></pre>
        <p class="mb-2 mt-4 text-xs text-muted">Anthropic 格式：</p>
        <pre class="surface-panel overflow-x-auto px-4 py-3"><code class="mono text-[11px] leading-relaxed">{{ messagesExample }}</code></pre>
      </div>

      <div>
        <h3 class="mb-3 text-sm font-medium">4. 查看用量</h3>
        <p class="text-sm leading-relaxed text-muted">
          每次请求都会记录模型、协议、状态码、耗时与是否流式。
          <template v-if="role === 'admin'">作为管理员，你可以查看全部账户的用量。</template>
          <template v-else>你只能看到自己的记录。</template>
          token 与成本当前为未知（上游未提供），页面会标注而不是显示 0。
        </p>
      </div>
    </section>

    <section v-else-if="activeTab === 'endpoints'">
      <div class="overflow-hidden rounded-[8px] border border-line">
        <table class="w-full text-left text-sm">
          <thead class="bg-paper-alt text-xs text-muted">
            <tr>
              <th class="px-4 py-3 font-normal">方法</th>
              <th class="px-4 py-3 font-normal">路径</th>
              <th class="px-4 py-3 font-normal">说明</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-line">
            <tr v-for="e in endpoints" :key="e.path">
              <td class="px-4 py-3">
                <span class="mono text-[11px]" :class="e.method === 'POST' ? 'text-accent' : 'text-muted'">{{ e.method }}</span>
              </td>
              <td class="px-4 py-3"><span class="mono text-xs">{{ e.path }}</span></td>
              <td class="px-4 py-3 text-xs text-muted">{{ e.note }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="mt-8">
        <h3 class="mb-3 text-sm font-medium">认证方式</h3>
        <ul class="space-y-2 text-sm leading-relaxed text-muted">
          <li class="flex items-start gap-2">
            <AppIcon name="lucide:dot" :size="14" class="mt-1 shrink-0 text-slate-400" />
            OpenAI 兼容端点用 <span class="mono text-xs">Authorization: Bearer &lt;key&gt;</span>
          </li>
          <li class="flex items-start gap-2">
            <AppIcon name="lucide:dot" :size="14" class="mt-1 shrink-0 text-slate-400" />
            Messages 端点接受 <span class="mono text-xs">x-api-key</span> 或 Bearer，二者不可同时发送
          </li>
          <li class="flex items-start gap-2">
            <AppIcon name="lucide:dot" :size="14" class="mt-1 shrink-0 text-slate-400" />
            请求必须是未压缩的 <span class="mono text-xs">application/json</span>，拒绝重复 JSON 字段
          </li>
        </ul>
      </div>

      <div class="mt-8 rounded-[8px] border border-line bg-paper-alt px-5 py-4">
        <p class="flex items-start gap-2 text-xs leading-relaxed text-muted">
          <AppIcon name="lucide:info" :size="14" class="mt-0.5 shrink-0" />
          不支持: Responses 的 <span class="mono">previous_response_id</span>、
          <span class="mono">conversation</span>、<span class="mono">background</span>、
          <span class="mono">store</span>，文件与容器引用，以及 WebSocket。
          使用这些会明确返回 501，不会静默降级。
        </p>
      </div>
    </section>

    <section v-else>
      <div class="overflow-hidden rounded-[8px] border border-line">
        <table class="w-full text-left text-sm">
          <thead class="bg-paper-alt text-xs text-muted">
            <tr>
              <th class="px-4 py-3 font-normal">状态码</th>
              <th class="px-4 py-3 font-normal">类型</th>
              <th class="px-4 py-3 font-normal">含义</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-line">
            <tr v-for="e in errors" :key="e.status + e.type">
              <td class="px-4 py-3"><span class="mono text-xs">{{ e.status }}</span></td>
              <td class="px-4 py-3"><span class="mono text-[11px] text-muted">{{ e.type }}</span></td>
              <td class="px-4 py-3 text-xs text-muted">{{ e.note }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="mt-8 rounded-[8px] border border-line bg-paper-alt px-5 py-4">
        <p class="flex items-start gap-2 text-xs leading-relaxed text-muted">
          <AppIcon name="lucide:shield-alert" :size="14" class="mt-0.5 shrink-0" />
          网关不重试。看到 5xx 时请自行决定是否重试——
          重复请求可能产生额外费用，且输出已开始传输时断流不会自动续接。
        </p>
      </div>
    </section>
  </div>
</template>
