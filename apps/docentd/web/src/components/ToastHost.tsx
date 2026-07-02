import { useEffect, useRef, useState } from "react";
import { registerToast } from "../lib/toast";

type ToastState = { msg: string; isErr: boolean; show: boolean };

export function ToastHost() {
  const [state, setState] = useState<ToastState>({ msg: "", isErr: false, show: false });
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => {
    registerToast((msg, isErr) => {
      setState({ msg, isErr, show: true });
      window.clearTimeout(timer.current);
      timer.current = window.setTimeout(
        () => setState((s) => ({ ...s, show: false })),
        2200,
      );
    });
    return () => {
      registerToast(null);
      window.clearTimeout(timer.current);
    };
  }, []);

  const cls = "toast" + (state.isErr ? " err" : "") + (state.show ? " show" : "");
  return (
    <div className={cls} role="status">
      {state.msg}
    </div>
  );
}
