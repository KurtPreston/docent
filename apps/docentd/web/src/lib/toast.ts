// Tiny global toast channel. A single <ToastHost> (rendered by <Layout>)
// subscribes; any module can fire a toast without threading a callback
// through props. Mirrors the behavior of the old dashboard's toast().
type Listener = (msg: string, isErr: boolean) => void;

let listener: Listener | null = null;

export function registerToast(l: Listener | null): void {
  listener = l;
}

export function toast(msg: string, isErr = false): void {
  if (listener) listener(msg, isErr);
}
