<script setup lang="ts">
import { onMounted, ref } from 'vue'
import AppIcon from '../components/AppIcon'
import { api, ApiError, hasAdminKey, type GatewayStatus } from '../api'

const status = ref<GatewayStatus | null>(null)
const errorText = ref('')
const loading = ref(true)

onMounted(async () => {
  if (!hasAdminKey()) {
    errorText.value = '未配置管理密钥'
    loading.value = false
    return
  }
  try {
    status.value = await api.status()
  } catch (error) {
    errorText.value = error instanceof ApiError ? error.message : '无法读取状态'
  } finally {
    loading.value = false
  }
})

const rows = [
  { k: '版本', key: 'version' },
  { k: '上游状态', key: 'upstream' },
  { k: '目录模型数', key: 'catalogCount' },
  { k: '网关重试次数', key: 'gatewayRetries' },
  { k: '活动策略版本', key: 'activePolicy' },
  { k: '存储', key: 'storage' },
] as const
</script>

<template>
  <div class="mx-auto max-w-[1280px]">
    <header class="mb-8">
      <p class="eyebrow mb-3 flex items-center gap-2">
        <AppIcon name="lucide:settings-2" :size="12" />
        设置
      </p>
      <h2 class="display-sm mb-3">运行状态与边界。</h2>
      <p class="max-w-2xl text-sm leading-relaxed text-muted">
        只读视图。配置项通过进程环境变量注入，不从界面修改，避免管理面成为提权路径。
      </p>
    </header>

    <el-alert v-if="errorText" type="warning" :closable="false" :title="errorText" />

    <section v-else v-loading="loading" class="overflow-hidden rounded-[8px] border border-line">
      <table class="w-full text-left text-sm">
        <tbody class="divide-y divide-line">
          <tr v-for="row in rows" :key="row.key">
            <td class="w-48 px-4 py-3 text-muted">{{ row.k }}</td>
            <td class="px-4 py-3">
              <span class="mono text-xs">{{ status ? (status[row.key] ?? 'null') : '—' }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <section class="mt-10">
      <h3 class="mb-4 text-sm font-medium">环境变量</h3>
      <div class="overflow-hidden rounded-[8px] border border-line">
        <table class="w-full text-left text-sm">
          <thead class="bg-paper-alt text-xs text-muted">
            <tr>
              <th class="px-4 py-3 font-normal">变量</th>
              <th class="px-4 py-3 font-normal">说明</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-line">
            <tr v-for="row in [
              { k: 'AIROUTER_LISTEN_ADDR', v: '数据面监听地址，默认 127.0.0.1:8080' },
              { k: 'AIROUTER_ADMIN_ADDR', v: '管理面监听地址，未设置则不启用' },
              { k: 'AIROUTER_ADMIN_KEY', v: '管理密钥，未设置时管理 API 返回 503' },
              { k: 'AIROUTER_CLIENTS_FILE', v: '客户端权限档案路径，仅存 Key 摘要' },
              { k: 'CPA_BASE_URL', v: '固定的 CPA origin' },
              { k: 'CPA_API_KEY', v: 'CPA 数据面 Key，禁止与网关 Key 相同' },
            ]" :key="row.k">
              <td class="px-4 py-3"><span class="mono text-xs">{{ row.k }}</span></td>
              <td class="px-4 py-3 text-muted">{{ row.v }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section class="mt-10 rounded-[8px] border border-line bg-paper-alt px-5 py-4">
      <p class="flex items-start gap-2 text-xs leading-relaxed text-muted">
        <AppIcon name="lucide:shield-alert" :size="14" class="mt-0.5 shrink-0" />
        管理密钥仅存于浏览器 sessionStorage，关闭标签即清除。密钥校验在服务端以常量时间比对，
        且管理面与数据面使用不同监听端口与不同密钥。
      </p>
    </section>
  </div>
</template>
