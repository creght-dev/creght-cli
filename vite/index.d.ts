import type { Plugin } from 'vite'

export type CreghtVitePluginOptions = {
  /**
   * Local Creght project root. Defaults to Vite's root.
   */
  root?: string
  /**
   * Creght API host used by the local /api proxy at runtime.
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

export declare function creght(options?: CreghtVitePluginOptions): Plugin
export default creght
