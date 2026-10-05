import sys

import bs4
import requests
from PIL import Image

print(f"Python profile OK: {sys.executable}")
print(f"requests={requests.__version__} bs4={bs4.__version__} pillow={Image.__version__}")
