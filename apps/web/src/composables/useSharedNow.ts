import { onBeforeUnmount, onMounted, ref } from 'vue'

const sharedNow = ref(new Date())
let listeners = 0
let timer: ReturnType<typeof setInterval> | undefined

export function useSharedNow() {
  onMounted(() => {
    if (++listeners === 1 && typeof setInterval !== 'undefined') {
      sharedNow.value = new Date()
      timer = setInterval(() => { sharedNow.value = new Date() }, 60_000)
    }
  })
  onBeforeUnmount(() => {
    if (--listeners <= 0) {
      listeners = 0
      if (timer !== undefined) {
        clearInterval(timer)
        timer = undefined
      }
    }
  })
  return sharedNow
}
