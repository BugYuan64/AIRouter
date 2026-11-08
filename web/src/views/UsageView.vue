<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppIcon from '../components/AppIcon'
import { api, ApiError, hasAdminKey, type UsageDetail, type UsageResponse } from '../api'

const summary = ref<UsageResponse | null>(null)
const details = ref<UsageDetail[]>([])
const loading = ref(true)
const errorText = ref('')
const storageMissing = ref(false)

const protocolLabel: Record<string, string> = {
  chat: 'Chat',
  responses: 'Responses',
  messages: 'Messages',
  count_tokens: 'Count',
}

async function load() {
  if (!hasAdminKey()) {
    errorText.value = '未配置管理密钥'
    loading.value = false
    return
  }
  loading.value = true
  try {
    const [s, d] = await Promise.all([api.usage(), api.usageRecent(50)])
    summary.value = s
    details.value = d.data
    errorText.value = ''
    storageMissing.value = false
  } catch (error) {
    if (error instanceof ApiError && error.type === 'storage_not_configured') {
      storageMissing.value = true
      errorText.value = ''
    } else {
      errorText.value = error instanceof ApiError ? error.message : '无法读取用量'
    }
  } finally {
    loading.value = false
  }
}

onMounted(load)

const isEmpty = computed(() => !loading.value && !errorText.value && !storageMissing.value && (summary.value?.requests ?? 0) === 0)

const metrics = computed(() => {
  const s = summary.value
  return [
    { label: '请求总数', icon: 'lucide:activity', value: s ? String(s.requests) : '—' },
    { label: '错误数', icon: 'lucide:triangle-alert', value: s ? String(s.errors) : '—' },
    { label: '流式请求', icon: 'lucide:waves', value: s ? String(s.rows.reduce((n, r) => n + r.streams, 0)) : '—' },
    { label: '区间', icon: 'lucide:calendar-range', value: s ? '近 30 天' : '—' },
  ]
})

function fmtTime(iso: string) {
  return new Date(iso).toLocaleString('zh-CN', { hour12: false })
}

function statusClass(status: number) {
  if (status >= 500) return 'text-rose-600'
  if (status >= 400) return 'text-amber-600'
  if (status === 499) return 'text-slate-500'
  return 'text-emerald-600'
}
</script>

<template>
  <div class="mx-auto max-w-[1280px]">
    <header class="mb-8">
      <p class="eyebrow mb-3 flex items-center gap-2">
        <AppIcon name="lucide:chart-column" :size="12" />
        用量管理
      </p>
      <h2 class="display-sm mb-3">用量按用户独立核算。</h2>
      <p class="max-w-2xl text-sm leading-relaxed text-muted">
        记录来自网关自身的转发路径，归属取自认证结果而非请求体。
        只保存元数据，不含请求正文、认证头或上游错误原文。
      </p>
    </header>

    <el-alert
      v-if="errorText"
      type="warning"
      :closable="false"
      :title="errorText"
      description="在左下角设置管理密钥后重试。"
    />

    <el-alert
      v-else-if="storageMissing"
      type="warning"
      :closable="false"
      title="未配置数据库"
      description="AIROUTER_DATABASE_URL 未设置，网关不记录用量。生成请求不受影响，但本页无数据可显示。"
    />

    <template v-else>
      <dl class="mb-8 grid grid-cols-2 gap-x-8 gap-y-6 border-y border-line py-6 md:grid-cols-4">
        <div v-for="m in metrics" :key="m.label">
          <dt class="eyebrow mb-2 flex items-center gap-1.5">
            <AppIcon :name="m.icon" :size="12" />
            {{ m.label }}
          </dt>
          <dd class="mono text-xl">{{ m.value }}</dd>
        </div>
      </dl>

      <!-- 只有区间内确实为零时才说"尚无记录"，避免把未加载显示成 0 -->
      <div v-if="isEmpty" class="rounded-[10px] border border-line bg-paper-alt px-6 py-10">
        <div class="flex items-start gap-4">
          <span class="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-slate-100 text-slate-500">
            <AppIcon name="lucide:inbox" :size="18" />
          </span>
          <div>
            <h3 class="mb-1.5 text-sm font-medium">尚无用量记录</h3>
            <p class="max-w-2xl text-sm leading-relaxed text-muted">
              近 30 天没有经过网关的生成请求。历史用量（网关上线前）从未被记录，
              因此不会在此出现数字 0 —— 那会让人误以为当时没有调用。
            </p>
          </div>
        </div>
      </div>

      <template v-else>
        <section class="mb-10">
          <h3 class="mb-4 text-sm font-medium">按调用者与协议</h3>
          <el-table :data="summary?.rows ?? []" row-key="clientId" empty-text="无数据">
            <el-table-column label="调用者" min-width="180">
              <template #default="{ row }"><span class="mono text-[13px]">{{ row.clientId }}</span></template>
            </el-table-column>
            <el-table-column label="协议" width="130">
              <template #default="{ row }">
                <span class="mono text-xs">{{ protocolLabel[row.protocol] ?? row.protocol }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="requests" label="请求" width="90" />
            <el-table-column label="错误" width="90">
              <template #default="{ row }">
                <span :class="row.errors > 0 ? 'text-rose-600' : 'text-muted'" class="mono text-xs">{{ row.errors }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="streams" label="流式" width="90" />
            <el-table-column label="平均耗时" width="120">
              <template #default="{ row }"><span class="mono text-xs">{{ row.avgLatencyMs }} ms</span></template>
            </el-table-column>
          </el-table>
        </section>

        <section>
          <div class="mb-4 flex items-center justify-between">
            <h3 class="text-sm font-medium">最近请求</h3>
            <span class="mono text-xs text-muted">
              token 与成本未解析，显示为「未知」
            </span>
          </div>
          <el-table :data="details" row-key="id" empty-text="无数据">
            <el-table-column label="时间" width="180">
              <template #default="{ row }"><span class="mono text-xs">{{ fmtTime(row.createdAt) }}</span></template>
            </el-table-column>
            <el-table-column label="调用者" width="120">
              <template #default="{ row }"><span class="mono text-xs">{{ row.clientId }}</span></template>
            </el-table-column>
            <el-table-column label="模型" min-width="160">
              <template #default="{ row }"><span class="mono text-xs">{{ row.model }}</span></template>
            </el-table-column>
            <el-table-column label="协议" width="110">
              <template #default="{ row }">
                <span class="mono text-xs">{{ protocolLabel[row.protocol] ?? row.protocol }}</span>
              </template>
            </el-table-column>
            <el-table-column label="状态" width="80">
              <template #default="{ row }">
                <span class="mono text-xs" :class="statusClass(row.status)">{{ row.status }}</span>
              </template>
            </el-table-column>
            <el-table-column label="流式" width="70">
              <template #default="{ row }">
                <span class="text-xs text-muted">{{ row.stream ? '是' : '否' }}</span>
              </template>
            </el-table-column>
            <el-table-column label="耗时" width="100">
              <template #default="{ row }"><span class="mono text-xs">{{ row.latencyMs }} ms</span></template>
            </el-table-column>
            <el-table-column label="首字节" width="100">
              <template #default="{ row }">
                <span class="mono text-xs text-muted">{{ row.firstByteMs === null ? '—' : row.firstByteMs + ' ms' }}</span>
              </template>
            </el-table-column>
            <el-table-column label="token" width="90">
              <template #default>
                <span class="text-xs text-muted">未知</span>
              </template>
            </el-table-column>
          </el-table>
        </section>
      </template>
    </template>
  </div>
</template>
