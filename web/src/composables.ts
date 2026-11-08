import { onMounted, onUnmounted, ref } from 'vue'

/** 滚动进场：IntersectionObserver 一次性触发，prefers-reduced-motion 时直接可见（由 CSS 处理）。 */
export function useReveal() {
  const observer = ref<IntersectionObserver | null>(null)

  onMounted(() => {
    if (!('IntersectionObserver' in window)) {
      document.querySelectorAll('.reveal').forEach((el) => el.classList.add('is-visible'))
      return
    }
    observer.value = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            entry.target.classList.add('is-visible')
            observer.value?.unobserve(entry.target)
          }
        }
      },
      { rootMargin: '0px 0px -12% 0px', threshold: 0.1 },
    )
    document.querySelectorAll('.reveal').forEach((el) => observer.value?.observe(el))
  })

  onUnmounted(() => observer.value?.disconnect())
}

/** 当前滚动进度 0~1，用于顶部进度条。 */
export function useScrollProgress() {
  const progress = ref(0)
  const onScroll = () => {
    const max = document.documentElement.scrollHeight - window.innerHeight
    progress.value = max > 0 ? Math.min(1, window.scrollY / max) : 0
  }
  onMounted(() => {
    window.addEventListener('scroll', onScroll, { passive: true })
    onScroll()
  })
  onUnmounted(() => window.removeEventListener('scroll', onScroll))
  return progress
}
