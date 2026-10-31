import Select, { type SelectProps } from "./Select";

export default function VendorSelect(props: SelectProps) {
  return <Select placeholder="Select a vendor" {...props} />;
}
