import type { ComputedRef, InjectionKey } from 'vue'

// A cached tab can outlive its entry in the workspace. Keep its resource
// lifetime tied to that entry without coupling preview renderers to the store.
export const workspaceTabKeys: InjectionKey<ComputedRef<ReadonlySet<string>>> = Symbol('workspace-tab-keys')
