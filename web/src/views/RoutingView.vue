<script setup lang="ts">
import AppIcon from '../components/AppIcon'

const prerequisites = [
  { done: true, text: '响应提交点判定（committed 标记），首字节后禁止跨模型回退' },
  { done: false, text: '策略快照原子替换（atomic.Pointer + 先写库再切换）' },
  { done: false, text: '决策日志有界异步写入，溢出丢弃且不反压生成请求' },
]
</script>

<template>
  <div class="mx-auto max-w-[1280px]">
    <header class="mb-8">
      <p class="eyebrow mb-3 flex items-center gap-2">
        <AppIcon name="lucide:git-compare" :size="12" />
        路由决策
      </p>
      <h2 class="display-sm mb-3">每条决策都带排除原因。</h2>
      <p class="max-w-2xl text-sm leading-relaxed text-muted">
        规则路由尚未启用。开放自动路由前必须先完成三项前置，否则跨模型回退可能在响应已提交后发生。
      </p>
    </header>

    <section class="mb-10">
      <h3 class="mb-4 text-sm font-medium">开放前置条件</h3>
      <ul class="space-y-2">
        <li
          v-for="item in prerequisites"
          :key="item.text"
          class="flex items-start gap-3 rounded-[8px] border border-line px-4 py-3"
        >
          <AppIcon
            :name="item.done ? 'lucide:circle-check' : 'lucide:circle-dashed'"
            :size="16"
            class="mt-0.5 shrink-0"
            :class="item.done ? 'text-emerald-600' : 'text-slate-400'"
          />
          <span class="text-sm leading-relaxed" :class="item.done ? '' : 'text-muted'">{{ item.text }}</span>
        </li>
      </ul>
    </section>

    <div class="rounded-[10px] border border-line bg-paper-alt px-6 py-10">
      <div class="flex items-start gap-4">
        <span class="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-slate-100 text-slate-500">
          <AppIcon name="lucide:info" :size="18" />
        </span>
        <div>
          <h3 class="mb-1.5 text-sm font-medium">暂无决策记录</h3>
          <p class="max-w-2xl text-sm leading-relaxed text-muted">
            当前所有请求都是指定模型一对一转发，不经过选模。启用
            <span class="mono text-xs">airouter/auto</span>
            后，此处将显示每次决策的目标入口、策略版本与被排除候选的具体原因。
          </p>
        </div>
      </div>
    </div>
  </div>
</template>
