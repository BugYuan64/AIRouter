<script setup lang="ts">
import { onMounted, ref } from 'vue'
import AppIcon from '../components/AppIcon'
import { api, ApiError, hasAdminKey, type ClientAccount } from '../api'

const clients = ref<ClientAccount[]>([])
const loading = ref(true)
const errorText = ref('')
const storageMissing = ref(false)
const dialog = ref(false)
const form = ref({ id: '', label: '', models: '' })
const formError = ref('')
const createdKey = ref('')
const creating = ref(false)

async function load() {
  if (!hasAdminKey()) {
    errorText.value = '未配置管理密钥'
    loading.value = false
    return
  }
  loading.value = true
  try {
    const result = await api.keys()
    clients.value = result.data
    errorText.value = ''
    storageMissing.value = false
  } catch (error) {
    if (error instanceof ApiError && error.type === 'storage_not_configured') {
      storageMissing.value = true
      errorText.value = ''
    } else {
      errorText.value = error instanceof ApiError ? error.message : '无法读取账户'
    }
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function submit() {
  formError.value = ''
  const id = form.value.id.trim()
  const models = form.value.models.split(/[,\s]+/).map((m) => m.trim()).filter(Boolean)
  if (!id) {
    formError.value = '请填写账户 ID'
    return
  }
  if (models.length === 0) {
    formError.value = '至少填写一个允许的模型 ID'
    return
  }
  creating.value = true
  try {
    const result = await api.createKey({ id, label: form.value.label.trim(), models })
    createdKey.value = result.key
    form.value = { id: '', label: '', models: '' }
    await load()
  } catch (error) {
    formError.value = error instanceof ApiError ? error.message : '创建失败'
  } finally {
    creating.value = false
  }
}

async function revoke(id: string) {
  try {
    await api.revokeKey(id)
    await load()
  } catch (error) {
    errorText.value = error instanceof ApiError ? error.message : '吊销失败'
  }
}

function openDialog() {
  createdKey.value = ''
  formError.value = ''
  dialog.value = true
}
</script>

<template>
  <div class="mx-auto max-w-[1280px]">
    <header class="mb-8">
      <p class="eyebrow mb-3 flex items-center gap-2">
        <AppIcon name="lucide:key-round" :size="12" />
        API Key 管理
      </p>
      <h2 class="display-sm mb-3">每个用户一把独立 Key。</h2>
      <p class="max-w-2xl text-sm leading-relaxed text-muted">
        网关 Key 与 CPA 数据面 Key 分离。原始 Key 只在创建时返回一次，数据库仅保存其 SHA256 摘要，
        吊销后立即生效，无需重启。
      </p>
    </header>

    <el-alert v-if="errorText" type="warning" :closable="false" :title="errorText" />
    <el-alert
      v-else-if="storageMissing"
      type="warning"
      :closable="false"
      title="未配置数据库"
      description="AIROUTER_DATABASE_URL 未设置，Key 管理不可用。此时网关退回到 clients.json 静态认证。"
    />

    <template v-else>
      <div class="mb-6 flex items-center gap-3">
        <button
          class="inline-flex items-center gap-2 rounded-[8px] bg-ink px-4 py-2 text-sm text-white transition-opacity hover:opacity-85"
          @click="openDialog"
        >
          <AppIcon name="lucide:plus" :size="15" />
          新建 Key
        </button>
        <button
          class="inline-flex items-center gap-2 rounded-[8px] border border-line px-3.5 py-2 text-sm transition-colors hover:border-ink"
          @click="load"
        >
          <AppIcon name="lucide:refresh-cw" :size="14" />
          刷新
        </button>
        <span class="mono ml-auto text-xs text-muted">共 {{ clients.length }} 个账户</span>
      </div>

      <el-table v-loading="loading" :data="clients" row-key="id" empty-text="尚无账户">
        <el-table-column label="账户 ID" min-width="150">
          <template #default="{ row }">
            <span class="mono text-[13px]">{{ row.id }}</span>
            <span v-if="row.isAdmin" class="ml-2 rounded bg-slate-100 px-1.5 py-0.5 text-[10px] text-slate-600">管理员</span>
          </template>
        </el-table-column>
        <el-table-column label="备注" width="140">
          <template #default="{ row }"><span class="text-xs text-muted">{{ row.label || '—' }}</span></template>
        </el-table-column>
        <el-table-column label="Key 前缀" width="140">
          <template #default="{ row }">
            <span class="mono text-xs text-muted">{{ row.keyPrefix }}</span>
          </template>
        </el-table-column>
        <el-table-column label="允许模型" min-width="200">
          <template #default="{ row }">
            <span v-for="m in row.models" :key="m" class="mono mr-1.5 inline-block rounded bg-paper-alt px-1.5 py-0.5 text-[11px]">
              {{ m }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <span class="text-xs" :class="row.status === 'active' ? 'text-emerald-600' : 'text-rose-600'">
              {{ row.status === 'active' ? '有效' : '已吊销' }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="110">
          <template #default="{ row }">
            <button
              v-if="row.status === 'active'"
              class="text-xs text-rose-600 hover:underline"
              @click="revoke(row.id)"
            >吊销</button>
            <span v-else class="text-xs text-muted">—</span>
          </template>
        </el-table-column>
      </el-table>
    </template>

    <el-dialog v-model="dialog" title="新建 API Key" width="520px">
      <div v-if="createdKey" class="space-y-3">
        <p class="text-sm text-muted">Key 已创建。它只显示这一次，关闭后无法再次获取：</p>
        <div class="rounded-[8px] border border-line bg-paper-alt px-3 py-3">
          <code class="mono break-all text-xs">{{ createdKey }}</code>
        </div>
        <p class="flex items-start gap-2 text-xs text-amber-700">
          <AppIcon name="lucide:triangle-alert" :size="14" class="mt-0.5 shrink-0" />
          请立即保存到安全位置。数据库只存摘要，无法还原。
        </p>
        <button
          class="w-full rounded-[8px] bg-ink px-4 py-2 text-sm text-white"
          @click="dialog = false"
        >我已保存</button>
      </div>

      <div v-else class="space-y-4">
        <div>
          <label class="mb-1.5 block text-xs text-muted">账户 ID</label>
          <el-input v-model="form.id" placeholder="如 alice" />
        </div>
        <div>
          <label class="mb-1.5 block text-xs text-muted">备注（可选）</label>
          <el-input v-model="form.label" placeholder="如 Alice 的个人密钥" />
        </div>
        <div>
          <label class="mb-1.5 block text-xs text-muted">允许的模型 ID（逗号或空格分隔）</label>
          <el-input v-model="form.models" placeholder="gpt-6.1-sol, glm-5.3" />
          <p class="mt-1.5 text-xs text-muted">必须是 CPA 目录中的精确 ID，不支持通配符。</p>
        </div>
        <p v-if="formError" class="text-xs text-rose-600">{{ formError }}</p>
        <div class="flex justify-end gap-2 pt-2">
          <button class="rounded-[8px] border border-line px-4 py-2 text-sm" @click="dialog = false">取消</button>
          <button
            class="rounded-[8px] bg-ink px-4 py-2 text-sm text-white disabled:opacity-50"
            :disabled="creating"
            @click="submit"
          >{{ creating ? '创建中…' : '创建' }}</button>
        </div>
      </div>
    </el-dialog>
  </div>
</template>
