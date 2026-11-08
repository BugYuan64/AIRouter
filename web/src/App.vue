<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from './components/AppIcon'
import {
  api,
  ApiError,
  hasAdminKey,
  setAdminKey,
  useRole,
  type GatewayStatus,
} from './api'

const route = useRoute()
const router = useRouter()

const isLogin = computed(() => route.name === 'login')
// 角色用响应式 ref，登录后侧栏立即切换；不再依赖手工 tick
const roleRef = useRole()
const isAdmin = computed(() => roleRef.value === 'admin')

/** 客户端只保留自己的用量、账户与文档；管理员额外可见全局与配置项。 */
const nav = computed(() => {
  const items = [
    { to: '/', label: '概览', icon: 'lucide:layout-dashboard', exact: true, adminOnly: false },
    { to: '/models', label: '模型列表', icon: 'lucide:boxes', adminOnly: false },
    { to: '/usage', label: '用量管理', icon: 'lucide:chart-column', adminOnly: false },
    { to: '/keys', label: 'API Key 管理', icon: 'lucide:key-round', adminOnly: false },
    { to: '/docs', label: '使用文档', icon: 'lucide:book-open', adminOnly: false },
    { to: '/routing', label: '路由决策', icon: 'lucide:git-compare', adminOnly: true },
    { to: '/settings', label: '设置', icon: 'lucide:settings-2', adminOnly: true },
  ]
  return items.filter((i) => !i.adminOnly || isAdmin.value)
})

const status = ref<GatewayStatus | null>(null)
const statusError = ref('')
const clientId = ref('')

const currentTitle = computed(() => (route.meta.title as string) ?? '控制台')

onMounted(() => {
  if (!isLogin.value) refreshStatus()
})

async function refreshStatus() {
  if (!hasAdminKey()) return
  try {
    const [s, me] = await Promise.all([api.status(), api.whoami()])
    status.value = s
    clientId.value = me.clientId
    // 以服务端返回的角色为准，修正本地存储可能的偏差
    roleRef.value = me.role
    statusError.value = ''
  } catch (error) {
    status.value = null
    if (error instanceof ApiError && error.status === 401) {
      statusError.value = '身份已失效'
      setAdminKey('')
      router.replace('/login')
    } else if (error instanceof ApiError && error.status === 503) {
      statusError.value = '服务端未配置'
    } else {
      statusError.value = '无法连接管理接口'
    }
  }
}

function logout() {
  setAdminKey('')
  router.replace('/login')
}
</script>

<template>
  <!-- 登录页不套壳，避免未认证时暴露导航结构 -->
  <RouterView v-if="isLogin" />

  <div v-else class="flex min-h-screen bg-paper">
    <aside class="sticky top-0 hidden h-screen w-60 shrink-0 flex-col border-r border-line bg-paper-alt lg:flex">
      <div class="border-b border-line px-6 py-5">
        <RouterLink to="/" class="flex items-center gap-2">
          <AppIcon name="lucide:route" :size="20" class="text-accent" />
          <span class="mono text-sm font-semibold tracking-tight">AIRouter</span>
        </RouterLink>
      </div>
      <nav class="flex-1 overflow-y-auto p-3">
        <ul class="space-y-0.5">
          <li v-for="item in nav" :key="item.to">
            <RouterLink
              :to="item.to"
              class="group flex items-center gap-3 rounded-[6px] px-3 py-2 text-sm transition-colors"
              :class="
                (item.exact ? route.path === item.to : route.path.startsWith(item.to))
                  ? 'bg-white font-medium text-ink shadow-sm'
                  : 'text-muted hover:bg-white/60 hover:text-ink'
              "
            >
              <AppIcon :name="item.icon" :size="16" />
              {{ item.label }}
            </RouterLink>
          </li>
        </ul>
      </nav>
      <div class="border-t border-line p-3">
        <div class="mb-2 flex items-center gap-2 px-3 py-1">
          <span
            class="rounded px-1.5 py-0.5 text-[10px]"
            :class="isAdmin ? 'bg-ink text-white' : 'bg-slate-200 text-slate-700'"
          >{{ isAdmin ? '管理员' : '客户端' }}</span>
          <span v-if="clientId" class="mono truncate text-[11px] text-muted">{{ clientId }}</span>
        </div>
        <button
          class="flex w-full items-center gap-3 rounded-[6px] px-3 py-2 text-sm text-muted transition-colors hover:bg-white/60 hover:text-ink"
          @click="logout"
        >
          <AppIcon name="lucide:log-out" :size="16" />
          退出登录
        </button>
      </div>
    </aside>

    <div class="flex min-w-0 flex-1 flex-col">
      <header class="sticky top-0 z-30 border-b border-line bg-paper/90 backdrop-blur">
        <div class="flex items-center gap-4 px-6 py-4 md:px-10">
          <h1 class="text-base font-medium">{{ currentTitle }}</h1>
          <div class="ml-auto flex items-center gap-3">
            <span class="mono hidden text-xs sm:inline" :class="statusError ? 'text-amber-600' : 'text-muted'">
              {{ statusError || (status ? `v${status.version} · 目录 ${status.catalogCount}` : '') }}
            </span>
            <span class="h-2 w-2 rounded-full" :class="statusError ? 'bg-amber-500' : 'bg-emerald-500'" />
          </div>
        </div>
      </header>

      <main class="flex-1 px-6 py-10 md:px-10 md:py-12">
        <RouterView />
      </main>

      <footer class="border-t border-line px-6 py-6 md:px-10">
        <p class="mono text-xs text-muted">
          凭证与 CPA 分离 · 网关不重试 · 不跨模型拼接输出
        </p>
      </footer>
    </div>
  </div>
</template>
