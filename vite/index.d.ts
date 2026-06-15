import type { Plugin } from 'vite'

export type CreghVitePluginOptions = {
  /**
   * Local Cregh project root. Defaults to Vite's root.
   */
  root?: string
  /**
   * Cregh API host used by the local /api proxy at runtime.
   */
  apiHost?: string
  /**
   * Project id for runtime CMS/form requests.
   */
  projectId?: string
  /**
   * Optional CLI auth token. When set, local /api proxy sends it as Bearer.
   */
  token?: string
  /**
   * Platform import map entries. `creght dev` fills this from server system info.
   * Manually configured entries override the built-in fallback when no server map is provided.
   */
  importMap?: Record<string, string>
}

export declare function creght(options?: CreghVitePluginOptions): Plugin
export default creght
