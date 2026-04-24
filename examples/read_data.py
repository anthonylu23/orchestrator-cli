import pathlib
import sys


data_path = pathlib.Path(sys.argv[1])
print(data_path.read_text(encoding="utf-8").strip())
