import { createRouter, createWebHistory } from 'vue-router'
import { hasAdminKey } from './api'

// history 模式：URL 不带 #，形如 /login、/models。
// 前提是服务端对未知路径回落到 index.html（admin.spaHandler 已实现），
// 否则刷新页面会 404。
const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('./views/LoginView.vue'), meta: { title: '登录', public: true } },
    { path: '/', name: 'overview', component: () => import('./views/OverviewView.vue'), meta: { title: '概览' } },
    { path: '/models', name: 'models', component: () => import('./views/ModelsView.vue'), meta: { title: '模型列表' } },
    { path: '/keys', name: 'keys', component: () => import('./views/KeysView.vue'), meta: { title: 'API Key 管理' } },
    { path: '/usage', name: 'usage', component: () => import('./views/UsageView.vue'), meta: { title: '用量管理' } },
    { path: '/docs', name: 'docs', component: () => import('./views/DocsView.vue'), meta: { title: '使用文档' } },
    { path: '/routing', name: 'routing', component: () => import('./views/RoutingView.vue'), meta: { title: '路由决策' } },
    { path: '/settings', name: 'settings', component: () => import('./views/SettingsView.vue'), meta: { title: '设置' } },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

// 未登录一律跳到登录页，避免未认证时发出必然 401 的请求。
router.beforeEach((to) => {
  if (!to.meta.public && !hasAdminKey()) {
    return { name: 'login' }
  }
  if (to.name === 'login' && hasAdminKey()) {
    return { name: 'overview' }
  }
  return true
})

router.afterEach((to) => {
  const title = (to.meta.title as string | undefined) ?? '控制台'
  document.title = `${title} · AIRouter`
})

export default router
