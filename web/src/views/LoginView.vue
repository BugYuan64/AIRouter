<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import AppIcon from '../components/AppIcon'
import { ApiError, detectRole, setAdminKey } from '../api'

const router = useRouter()
const key = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  error.value = ''
  const value = key.value.trim()
  if (!value) {
    error.value = '请输入 Key'
    return
  }
  busy.value = true
  try {
    const { role } = await detectRole(value)
    setAdminKey(value, role)
    router.replace('/')
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : '登录失败，请重试'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="flex min-h-screen items-center justify-center bg-paper px-6">
    <div class="w-full max-w-md">
      <div class="mb-10 flex items-center gap-2.5">
        <AppIcon name="lucide:route" :size="22" class="text-accent" />
        <span class="mono text-base font-semibold tracking-tight">AIRouter</span>
      </div>

      <h1 class="display-sm mb-3">登录控制台。</h1>
      <p class="mb-8 text-sm leading-relaxed text-muted">
        使用你的 API Key 登录。管理员 Key 可管理全部账户；客户端 Key 只能查看自己的用量与账户。
      </p>

      <form @submit.prevent="submit">
        <label class="mb-2 block text-xs text-muted">API Key</label>
        <el-input
          v-model="key"
          type="password"
          show-password
          placeholder="sk-ar-... 或管理密钥"
          size="large"
          @keyup.enter="submit"
        />
        <p v-if="error" class="mt-3 flex items-center gap-1.5 text-xs text-rose-600">
          <AppIcon name="lucide:circle-alert" :size="13" />
          {{ error }}
        </p>

        <button
          type="submit"
          class="mt-6 w-full rounded-[8px] bg-ink px-4 py-2.5 text-sm text-white transition-opacity hover:opacity-85 disabled:opacity-50"
          :disabled="busy"
        >
          {{ busy ? '验证中…' : '登录' }}
        </button>
      </form>

      <p class="mt-8 flex items-start gap-2 text-xs leading-relaxed text-muted">
        <AppIcon name="lucide:shield-check" :size="14" class="mt-0.5 shrink-0" />
        Key 只保存在当前标签页的 sessionStorage，关闭即清除。
        服务端只存 SHA256 摘要，无法还原原始 Key。
      </p>
    </div>
  </div>
</template>
