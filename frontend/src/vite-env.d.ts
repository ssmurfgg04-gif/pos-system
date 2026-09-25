/// <reference types="vite/client" />

// jsbarcode ships no bundled types — we only use the canvas call signature.
declare module 'jsbarcode' {
  interface JsBarcodeOptions {
    format?: string
    width?: number
    height?: number
    displayValue?: boolean
    fontSize?: number
    margin?: number
    [key: string]: unknown
  }
  function JsBarcode(element: unknown, text: string, options?: JsBarcodeOptions): void
  export default JsBarcode
}
