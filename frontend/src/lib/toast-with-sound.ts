import { toast } from "sonner";
import { playSuccessSound, playErrorSound, playWarningSound, playInfoSound, } from "./audio";
import { logger } from "./logger";
import { getSettings } from "./settings";
const toastStyle = {
    className: "font-mono lowercase",
};
type ToastData = Parameters<typeof toast.success>[1];
const defaultToastDurations = {
    success: 5000,
    info: 6000,
    warning: 9000,
    error: 12000,
    message: 6000,
};
const isSfxEnabled = () => getSettings().sfxEnabled;
export const toastWithSound = {
    success: (message: string, data?: ToastData) => {
        const msg = message.toLowerCase();
        logger.success(msg);
        if (isSfxEnabled())
            playSuccessSound();
        return toast.success(msg, { duration: defaultToastDurations.success, ...toastStyle, ...data });
    },
    error: (message: string, data?: ToastData) => {
        const msg = message.toLowerCase();
        logger.error(msg);
        if (isSfxEnabled())
            playErrorSound();
        return toast.error(msg, { duration: defaultToastDurations.error, ...toastStyle, ...data });
    },
    warning: (message: string, data?: ToastData) => {
        const msg = message.toLowerCase();
        logger.warning(msg);
        if (isSfxEnabled())
            playWarningSound();
        return toast.warning(msg, { duration: defaultToastDurations.warning, ...toastStyle, ...data });
    },
    info: (message: string, data?: ToastData) => {
        const msg = message.toLowerCase();
        logger.info(msg);
        if (isSfxEnabled())
            playInfoSound();
        return toast.info(msg, { duration: defaultToastDurations.info, ...toastStyle, ...data });
    },
    message: (message: string, data?: ToastData) => {
        const msg = message.toLowerCase();
        logger.info(msg);
        if (isSfxEnabled())
            playInfoSound();
        return toast(msg, { duration: defaultToastDurations.message, ...toastStyle, ...data });
    },
    silentInfo: (message: string, data?: ToastData) => {
        const msg = message.toLowerCase();
        logger.info(msg);
        return toast.info(msg, { duration: defaultToastDurations.info, ...toastStyle, ...data });
    },
    dismiss: (id?: string | number) => toast.dismiss(id),
    toast: toast,
};
