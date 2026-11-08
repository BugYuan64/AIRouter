<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppIcon from '../components/AppIcon'
import { api, ApiError, hasAdminKey, type ModelProfile } from '../api'

const models = ref<ModelProfile[]>([])
const loading = ref(true)
const errorText = ref('')
const stale = ref(false)
const keyword = ref('')
const onlyOwned = ref('')

const capLabel: Record<string, string> = {
  supported: '支持',
  unsupported: '不支持',
  unknown: '未知',
}
const capClass: Record<string, string> = {
  supported: 'bg-emerald-50 text-emerald-700',
  unsupported: 'bg-rose-50 text-rose-700',
  unknown: 'bg-slate-100 text-slate-500',
}

async function load() {
  if (!hasAdminKey()) {
    errorText.value = '未配置管理密钥'
    loading.value = false
    return
  }
  loading.value = true
  try {
    const result = await api.models()
    models.value = result.data
    stale.value = result.stale === true
    errorText.value = ''
  } catch (error) {
    errorText.value = error instanceof ApiError ? error.message : '无法读取模型目录'
  } finally {
    loading.value = false
  }
}

onMounted(load)

const owners = computed(() => [...new Set(models.value.map((m) => m.ownedBy || '未提供'))].sort())

const filtered = computed(() => {
  const q = keyword.value.trim().toLowerCase()
  return models.value.filter((m) => {
    if (q && !m.id.toLowerCase().includes(q)) return false
    if (onlyOwned.value && (m.ownedBy || '未提供') !== onlyOwned.value) return false
    return true
  })
})

const verifiedCount = computed(
  () => models.value.filter((m) => [m.chat, m.responses, m.messages].includes('supported')).length,
)
</script>

<template>
  <div class="mx-auto max-w-[1280px]">
    <header class="mb-8">
      <p class="eyebrow mb-3 flex items-center gap-2">
        <AppIcon name="lucide:boxes" :size="12" />
        模型目录
      </p>
      <h2 class="display-sm mb-3">能力为三态，未知不等于支持。</h2>
      <p class="max-w-2xl text-sm leading-relaxed text-muted">
        此页只呈现 CPA 目录中的模型 ID 与归属。能力字段全部标记为未知，
        因为"出现在目录里"不构成能力证据；需经专项验收后才能改为支持或不支持。
      </p>
    </header>

    <div class="mb-6 flex flex-wrap items-center gap-3">
      <el-input v-model="keyword" placeholder="搜索模型 ID" clearable class="max-w-64" />
      <el-select v-model="onlyOwned" placeholder="全部归属" clearable class="w-40">
        <el-option v-for="o in owners" :key="o" :label="o" :value="o" />
      </el-select>
      <button
        class="inline-flex items-center gap-2 rounded-[8px] border border-line px-3.5 py-2 text-sm transition-colors hover:border-ink"
        @click="load"
      >
        <AppIcon name="lucide:refresh-cw" :size="14" />
        刷新
      </button>
      <span class="mono ml-auto text-xs text-muted">
        共 {{ models.length }} 个 · 已确认 {{ verifiedCount }} 个
      </span>
    </div>

    <el-alert
      v-if="errorText"
      type="warning"
      :closable="false"
      :title="errorText"
      description="在左下角设置管理密钥后重试。"
    />
    <el-alert
      v-else-if="stale"
      class="mb-4"
      type="info"
      :closable="false"
      title="显示的是缓存目录"
      description="上游暂时不可读，以下数据来自上一次成功读取，可能已经过期。"
    />

    <el-table v-else v-loading="loading" :data="filtered" row-key="id" empty-text="目录为空">
      <el-table-column prop="id" label="模型 ID" min-width="230">
        <template #default="{ row }">
          <span class="mono text-[13px]">{{ row.id }}</span>
        </template>
      </el-table-column>
      <el-table-column label="归属" width="140">
        <template #default="{ row }">
          <span class="mono text-xs text-muted">{{ row.ownedBy || '未提供' }}</span>
        </template>
      </el-table-column>
      <el-table-column v-for="key in ['chat', 'responses', 'messages', 'tools']" :key="key" :label="key" width="118">
        <template #default="{ row }">
          <span class="inline-block rounded px-1.5 py-0.5 text-[11px]" :class="capClass[row[key]]">
            {{ capLabel[row[key]] }}
          </span>
        </template>
      </el-table-column>
      <el-table-column label="上下文" width="110">
        <template #default="{ row }">
          <span class="mono text-xs text-muted">
            {{ row.contextWindow ? row.contextWindow.toLocaleString() : '未知' }}
          </span>
        </template>
      </el-table-column>
      <el-table-column label="验证日期" width="120">
        <template #default="{ row }">
          <span class="mono text-xs text-muted">{{ row.verifiedAt || '未验证' }}</span>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>
