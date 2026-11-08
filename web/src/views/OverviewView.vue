<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppIcon from '../components/AppIcon'
import { api, ApiError, hasAdminKey, type GatewayStatus } from '../api'
import { useReveal } from '../composables'

const status = ref<GatewayStatus | null>(null)
const loading = ref(true)
const errorText = ref('')

useReveal()

onMounted(async () => {
  if (!hasAdminKey()) {
    errorText.value = '未配置管理密钥'
    loading.value = false
    return
  }
  try {
    status.value = await api.status()
  } catch (error) {
    errorText.value = error instanceof ApiError ? error.message : '无法连接管理接口'
  } finally {
    loading.value = false
  }
})

const protocols = [
  {
    n: '01',
    name: 'Chat Completions',
    path: 'POST /v1/chat/completions',
    icon: 'lucide:message-square',
    body: 'OpenAI 原生 JSON 与 SSE 透传，正文逐字节保持，不转换、不改写模型。',
  },
  {
    n: '02',
    name: 'Responses',
    path: 'POST /v1/responses',
    icon: 'lucide:sparkles',
    body: '完整输入随请求提交。有状态引用、后台执行与 WebSocket 当前明确拒绝。',
  },
  {
    n: '03',
    name: 'Anthropic Messages',
    path: 'POST /v1/messages',
    icon: 'lucide:braces',
    body: '接受 Bearer 或 x-api-key，原生事件流含 count_tokens 转发。',
  },
]

const roadmap = [
  { id: 'P0', title: '准备与设计', state: 'done', note: '环境核验与架构确定' },
  { id: 'P1', title: 'API 基础入口', state: 'done', note: '认证、健康检查、目录过滤' },
  { id: 'P2', title: '三协议转发', state: 'partial', note: '四模型 13 项通过' },
  { id: 'P2.5', title: '控制面基座', state: 'active', note: '管理面与前端已落地' },
  { id: 'P3', title: '规则路由', state: 'todo', note: '能力档案与可解释决策' },
  { id: 'P4', title: '语义路由', state: 'todo', note: '效果优先与离线评测' },
  { id: 'P5', title: '部署验收', state: 'todo', note: 'TLS、灰度、回滚' },
]

const stateStyle: Record<string, { dot: string; text: string }> = {
  done: { dot: 'bg-emerald-500', text: '已完成' },
  partial: { dot: 'bg-amber-500', text: '部分完成' },
  active: { dot: 'bg-accent', text: '进行中' },
  todo: { dot: 'bg-slate-300', text: '未开始' },
}

const metrics = computed(() => {
  const s = status.value
  return [
    {
      label: '上游状态',
      icon: 'lucide:activity',
      value: loading.value ? '—' : errorText.value ? '未连接' : s?.upstream === 'ok' ? '正常' : s?.upstream ?? '—',
    },
    { label: '目录模型', icon: 'lucide:boxes', value: loading.value || errorText.value ? '—' : String(s?.catalogCount ?? 0) },
    { label: '网关重试', icon: 'lucide:rotate-ccw', value: loading.value || errorText.value ? '—' : String(s?.gatewayRetries ?? 0) },
    { label: '策略版本', icon: 'lucide:git-branch', value: s?.activePolicy ?? '未启用' },
  ]
})
</script>

<template>
  <div class="mx-auto max-w-[1280px]">
    <!-- Hero -->
    <section class="flex min-h-[52vh] flex-col justify-center pb-16">
      <p class="eyebrow mb-6 flex items-center gap-2">
        <AppIcon name="lucide:radar" :size="13" />
        指定模型转发 · API 网关
      </p>
      <h1 class="display max-w-3xl">一次转发，<br />忠于原始字节。</h1>
      <p class="mt-8 max-w-xl text-base leading-relaxed text-muted">
        以 CLIProxyAPI 为执行内核。保留模型、原始 JSON 正文与原生协议，不做跨协议转换，
        不启用智能选模、网关重试或跨模型回退。
      </p>
      <div class="mt-10 flex flex-wrap items-center gap-3">
        <RouterLink
          to="/models"
          class="inline-flex items-center gap-2 rounded-[8px] bg-ink px-5 py-2.5 text-sm text-white transition-opacity hover:opacity-85"
        >
          <AppIcon name="lucide:boxes" :size="15" />
          查看模型目录
        </RouterLink>
        <RouterLink
          to="/keys"
          class="inline-flex items-center gap-2 rounded-[8px] border border-line px-5 py-2.5 text-sm transition-colors hover:border-ink"
        >
          <AppIcon name="lucide:key-round" :size="15" />
          API Key 管理
        </RouterLink>
      </div>
    </section>

    <!-- 指标 -->
    <dl class="grid grid-cols-2 gap-x-8 gap-y-8 border-t border-line pt-8 md:grid-cols-4">
      <div v-for="m in metrics" :key="m.label">
        <dt class="eyebrow mb-2 flex items-center gap-1.5">
          <AppIcon :name="m.icon" :size="12" />
          {{ m.label }}
        </dt>
        <dd class="mono text-xl">{{ m.value }}</dd>
      </div>
    </dl>

    <el-alert
      v-if="errorText"
      class="mt-8"
      type="warning"
      :closable="false"
      :title="errorText"
      description="在左下角设置管理密钥后，本页数据自动填充。"
    />

    <!-- 协议：浅色柔和面板 -->
    <section class="mt-20 reveal">
      <div class="surface-panel px-7 py-12 md:px-10">
        <p class="eyebrow-accent mb-4 flex items-center gap-2">
          <AppIcon name="lucide:plug" :size="12" />
          协议入口
        </p>
        <h2 class="display-sm max-w-2xl">三种原生协议，同一条转发路径。</h2>
        <div class="mt-12 grid gap-10 md:grid-cols-3">
          <article v-for="p in protocols" :key="p.n" class="border-t border-surface-line pt-5">
            <div class="mb-3 flex items-center gap-2">
              <AppIcon :name="p.icon" :size="14" class="text-accent" />
              <span class="mono text-xs text-muted">{{ p.n }}</span>
            </div>
            <h3 class="mb-1.5 text-base font-medium">{{ p.name }}</h3>
            <p class="mono mb-3 text-[11px] text-muted">{{ p.path }}</p>
            <p class="text-[13px] leading-relaxed text-muted">{{ p.body }}</p>
          </article>
        </div>
      </div>
    </section>

    <!-- 阶段 -->
    <section class="mt-20 reveal pb-4">
      <p class="eyebrow mb-4 flex items-center gap-2">
        <AppIcon name="lucide:milestone" :size="12" />
        实施阶段
      </p>
      <h2 class="display-sm mb-10 max-w-2xl">先证明直连，再谈自动选模。</h2>
      <ol class="grid gap-px overflow-hidden rounded-[8px] bg-line sm:grid-cols-2 lg:grid-cols-4">
        <li v-for="r in roadmap" :key="r.id" class="bg-paper p-5">
          <div class="mb-3 flex items-center gap-2">
            <span class="h-1.5 w-1.5 rounded-full" :class="stateStyle[r.state].dot" />
            <span class="mono text-[11px] text-muted">{{ stateStyle[r.state].text }}</span>
          </div>
          <p class="mono mb-1 text-xs">{{ r.id }}</p>
          <p class="mb-1.5 text-sm font-medium">{{ r.title }}</p>
          <p class="text-xs leading-relaxed text-muted">{{ r.note }}</p>
        </li>
        <li class="hidden bg-paper lg:block" aria-hidden="true" />
      </ol>
    </section>
  </div>
</template>
