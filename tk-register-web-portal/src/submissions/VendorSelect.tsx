import { t } from '../i18n';
import Select, { type SelectProps } from "./Select";

export default function VendorSelect(props: SelectProps) {
  return <Select placeholder={t("Select a vendor")} {...props} />;
}
